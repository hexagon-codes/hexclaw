package instances

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/hexagon-codes/toolkit/util/idgen"
)

// PhotoIntakeKey names the physical message independently of an expiring media handle.
type PhotoIntakeKey struct {
	Platform, InstanceID, ChatID, ProviderMessageID string
}

// PhotoIntakeRoute freezes the verified business target and the first notice text.
type PhotoIntakeRoute struct {
	Kind, AgentName, BindingID, SessionID, NoticeText, Locale string
	OwnerScope, Provider, Model, RequestID                    string
	DomainData                                                json.RawMessage `json:"DomainData,omitempty"`
}

type PhotoIntakeInput struct {
	Key                                            PhotoIntakeKey
	SenderID, Content, ReplyTo, UserName           string
	InstanceName, ConversationID, ConversationType string
	OriginalInstanceID                             string
	QuotedHomework                                 string
	QuotedHomeworkAmbiguous                        bool
	Route                                          PhotoIntakeRoute
	PlatformCreatedAtMS, ReceivedAtMS              int64
	PageSignatures                                 []string
}

// PhotoIntakeState is a durable physical operation. A missing timestamp means no such fact is known.
type PhotoIntakeState struct {
	Status            string `json:"status"`
	ExternalID        string `json:"external_id,omitempty"`
	FailureKind       string `json:"failure_kind,omitempty"`
	ResultRef         string `json:"result_ref,omitempty"`
	StartedAtMS       int64  `json:"started_at_ms,omitempty"`
	AcceptedAtMS      int64  `json:"accepted_at_ms,omitempty"`
	DeliveredAtMS     int64  `json:"delivered_at_ms,omitempty"`
	RecallStartedAtMS int64  `json:"recall_started_at_ms,omitempty"`
	RecalledAtMS      int64  `json:"recalled_at_ms,omitempty"`
	UpdatedAtMS       int64  `json:"updated_at_ms,omitempty"`
}

type PhotoIntakePage struct {
	Index                                                     int
	Signature, Name, MIME, Digest, FailureKind                string
	Size, StartedAtMS, DownloadedAtMS, StoredAtMS, FailedAtMS int64
	Data                                                      []byte
}

type PhotoIntakeReceipt struct {
	PhotoIntakeInput
	ID                             string
	CommittedAtMS, AssetsReadyAtMS int64
	Notice, Work, Final            PhotoIntakeState
	Pages                          []PhotoIntakePage
}

var ErrPhotoIntakeConflict = errors.New("IM photo intake conflicts with the original message")

type PhotoIntakeStore struct{ db *sql.DB }

func NewPhotoIntakeStore(db *sql.DB) *PhotoIntakeStore { return &PhotoIntakeStore{db: db} }

// Keep the received text and supported quote fields; transport envelopes contain expiring credentials.
type photoIntakeMessage struct {
	Content, ReplyTo, UserName, InstanceName, ConversationID, ConversationType, QuotedHomework string
	OriginalInstanceID                                                                         string
	QuotedHomeworkAmbiguous                                                                    bool
}

func intakeMessage(in PhotoIntakeInput) photoIntakeMessage {
	return photoIntakeMessage{
		Content: in.Content, ReplyTo: in.ReplyTo, UserName: in.UserName,
		InstanceName: in.InstanceName, OriginalInstanceID: in.OriginalInstanceID,
		ConversationID: in.ConversationID, ConversationType: in.ConversationType,
		QuotedHomework: in.QuotedHomework, QuotedHomeworkAmbiguous: in.QuotedHomeworkAmbiguous,
	}
}

func (m photoIntakeMessage) restore(in *PhotoIntakeInput) {
	in.Content, in.ReplyTo, in.UserName, in.InstanceName = m.Content, m.ReplyTo, m.UserName, m.InstanceName
	in.ConversationID, in.ConversationType, in.QuotedHomework = m.ConversationID, m.ConversationType, m.QuotedHomework
	in.QuotedHomeworkAmbiguous = m.QuotedHomeworkAmbiguous
	in.OriginalInstanceID = m.OriginalInstanceID
}

// DomainData is the existing frozen routing command, never callback metadata or a provider response.
func validatePhotoIntakeRoute(route PhotoIntakeRoute) error {
	if route.Kind != "ordinary" && route.Kind != "k12" {
		return errors.New("IM photo intake business route is required")
	}
	if len(route.DomainData) == 0 || bytes.Equal(bytes.TrimSpace(route.DomainData), []byte("null")) {
		return nil
	}
	var command struct {
		SchemaVersion       int    `json:"schema_version"`
		StatusNoticeVersion string `json:"status_notice_version"`
		MessageLocale       string `json:"message_locale"`
		SourceSessionID     string `json:"source_session_id"`
		MessageIntent       string `json:"message_intent"`
		Provider            string `json:"provider"`
		Model               string `json:"model"`
	}
	dec := json.NewDecoder(bytes.NewReader(route.DomainData))
	dec.DisallowUnknownFields()
	if dec.Decode(&command) != nil || dec.Decode(new(json.RawMessage)) != io.EOF {
		return errors.New("IM photo intake routing command has unsupported fields")
	}
	return nil
}

func validPhotoSignature(signature string) bool {
	raw := strings.TrimPrefix(signature, "sha256:")
	if len(raw) != 64 || raw != strings.ToLower(raw) {
		return false
	}
	_, err := hex.DecodeString(raw)
	return err == nil
}

const photoKeyWhere = `platform=? AND instance_id=? AND chat_id=? AND provider_message_id=?`
const photoSourceID = `(SELECT id FROM im_photo_intakes WHERE ` + photoKeyWhere + `)`

func (key PhotoIntakeKey) args() []any {
	return []any{key.Platform, key.InstanceID, key.ChatID, key.ProviderMessageID}
}

func (in PhotoIntakeInput) validate() error {
	k := in.Key
	for _, v := range []string{k.Platform, k.InstanceID, k.ChatID, k.ProviderMessageID, in.SenderID} {
		if strings.TrimSpace(v) == "" {
			return errors.New("IM photo intake source and sender are required")
		}
	}
	if in.ReceivedAtMS <= 0 || in.PlatformCreatedAtMS < 0 || len(in.PageSignatures) == 0 {
		return errors.New("IM photo intake requires a receive time and ordered pages")
	}
	if in.OriginalInstanceID != "" && in.OriginalInstanceID != k.InstanceID && in.OriginalInstanceID != in.InstanceName {
		return errors.New("IM photo intake instance does not match its authenticated source")
	}
	for _, sig := range in.PageSignatures {
		if !validPhotoSignature(sig) {
			return errors.New("IM photo intake page fingerprint is invalid")
		}
	}
	return validatePhotoIntakeRoute(in.Route)
}

// Register freezes the whole manifest and a single pending notice in one transaction.
func (s *PhotoIntakeStore) Register(ctx context.Context, in PhotoIntakeInput) (PhotoIntakeReceipt, bool, error) {
	if err := in.validate(); err != nil {
		return PhotoIntakeReceipt{}, false, err
	}
	route, err := json.Marshal(in.Route)
	if err != nil {
		return PhotoIntakeReceipt{}, false, err
	}
	msg, err := json.Marshal(intakeMessage(in))
	if err != nil {
		return PhotoIntakeReceipt{}, false, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return PhotoIntakeReceipt{}, false, err
	}
	defer func() { _ = tx.Rollback() }()
	id := idgen.NanoID()
	k := in.Key
	res, err := tx.ExecContext(ctx, `INSERT INTO im_photo_intakes(id,platform,instance_id,chat_id,provider_message_id,sender_id,message_json,route_json,page_count,platform_created_at_ms,received_at_ms,committed_at_ms)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,0) ON CONFLICT(platform,instance_id,chat_id,provider_message_id) DO NOTHING`,
		id, k.Platform, k.InstanceID, k.ChatID, k.ProviderMessageID, in.SenderID, string(msg), string(route), len(in.PageSignatures), in.PlatformCreatedAtMS, in.ReceivedAtMS)
	if err != nil {
		return PhotoIntakeReceipt{}, false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return PhotoIntakeReceipt{}, false, err
	}
	if n == 1 {
		for i, sig := range in.PageSignatures {
			if _, err = tx.ExecContext(ctx, `INSERT INTO im_photo_intake_pages(intake_id,page_index,signature) VALUES(?,?,?)`, id, i, sig); err != nil {
				return PhotoIntakeReceipt{}, false, err
			}
		}
	}
	stored, err := readPhotoIntake(ctx, tx, k)
	if err != nil {
		return PhotoIntakeReceipt{}, false, err
	}
	if n == 0 {
		if err = checkPhotoIntakeReplay(stored, in); err != nil {
			return PhotoIntakeReceipt{}, false, err
		}
	} else {
		// Date the complete envelope after obtaining the writer lock and inserting every manifest row.
		stored.CommittedAtMS = time.Now().UnixMilli()
		if _, err = tx.ExecContext(ctx, "UPDATE im_photo_intakes SET committed_at_ms=? WHERE id=?", stored.CommittedAtMS, id); err != nil {
			return PhotoIntakeReceipt{}, false, err
		}
	}
	if err = tx.Commit(); err != nil {
		return PhotoIntakeReceipt{}, false, err
	}
	return stored, n == 1, nil
}

func checkPhotoIntakeReplay(stored PhotoIntakeReceipt, in PhotoIntakeInput) error {
	// Renamed instances and changed agent configuration do not replace the original target.
	if stored.SenderID != in.SenderID || stored.Content != in.Content || stored.ReplyTo != in.ReplyTo ||
		stored.ConversationID != in.ConversationID || stored.ConversationType != in.ConversationType ||
		stored.QuotedHomework != in.QuotedHomework || stored.QuotedHomeworkAmbiguous != in.QuotedHomeworkAmbiguous ||
		len(stored.Pages) != len(in.PageSignatures) {
		return ErrPhotoIntakeConflict
	}
	for i, page := range stored.Pages {
		// A fresh handle can identify the same file. A saved digest makes a byte comparison possible.
		if page.Signature != in.PageSignatures[i] && page.Digest == "" {
			return fmt.Errorf("%w: changed source for unavailable page %d", ErrPhotoIntakeConflict, i)
		}
	}
	return nil
}

// Get reads one consistent manifest without loading the image BLOBs.
func (s *PhotoIntakeStore) Get(ctx context.Context, k PhotoIntakeKey) (PhotoIntakeReceipt, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return PhotoIntakeReceipt{}, err
	}
	defer func() { _ = tx.Rollback() }()
	r, err := readPhotoIntake(ctx, tx, k)
	if err != nil {
		return PhotoIntakeReceipt{}, err
	}
	return r, tx.Commit()
}

const photoPageColumns = `page_index,signature,name,mime,byte_size,digest,started_at_ms,downloaded_at_ms,stored_at_ms,failure_kind,failed_at_ms`

func scanPhotoPage(row interface{ Scan(...any) error }, data bool) (PhotoIntakePage, error) {
	var p PhotoIntakePage
	args := []any{&p.Index, &p.Signature, &p.Name, &p.MIME, &p.Size, &p.Digest, &p.StartedAtMS, &p.DownloadedAtMS, &p.StoredAtMS, &p.FailureKind, &p.FailedAtMS}
	if data {
		args = append(args, &p.Data)
	}
	err := row.Scan(args...)
	return p, err
}

func readPhotoIntake(ctx context.Context, tx *sql.Tx, k PhotoIntakeKey) (PhotoIntakeReceipt, error) {
	var r PhotoIntakeReceipt
	r.Key = k
	var pageCount int
	var message, route, notice, work, final string
	err := tx.QueryRowContext(ctx, `SELECT id,sender_id,message_json,route_json,page_count,platform_created_at_ms,received_at_ms,committed_at_ms,assets_ready_at_ms,notice_json,work_json,final_json
		FROM im_photo_intakes WHERE `+photoKeyWhere, k.args()...).Scan(&r.ID, &r.SenderID, &message, &route, &pageCount, &r.PlatformCreatedAtMS, &r.ReceivedAtMS, &r.CommittedAtMS, &r.AssetsReadyAtMS, &notice, &work, &final)
	if err != nil {
		return r, err
	}
	var m photoIntakeMessage
	for _, entry := range []struct {
		raw  string
		dest any
	}{{message, &m}, {route, &r.Route}, {notice, &r.Notice}, {work, &r.Work}, {final, &r.Final}} {
		if err = json.Unmarshal([]byte(entry.raw), entry.dest); err != nil {
			return r, fmt.Errorf("decode IM photo intake: %w", err)
		}
	}
	m.restore(&r.PhotoIntakeInput)
	if bytes.Equal(bytes.TrimSpace(r.Route.DomainData), []byte("null")) {
		r.Route.DomainData = nil
	}
	rows, err := tx.QueryContext(ctx, `SELECT `+photoPageColumns+` FROM im_photo_intake_pages WHERE intake_id=? ORDER BY page_index`, r.ID)
	if err != nil {
		return r, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		p, scanErr := scanPhotoPage(rows, false)
		if scanErr != nil {
			return r, scanErr
		}
		if p.Index != len(r.Pages) {
			return r, ErrPhotoIntakeConflict
		}
		r.Pages = append(r.Pages, p)
		r.PageSignatures = append(r.PageSignatures, p.Signature)
	}
	if err = rows.Err(); err != nil {
		return r, err
	}
	if len(r.Pages) != pageCount {
		return r, ErrPhotoIntakeConflict
	}
	return r, nil
}
