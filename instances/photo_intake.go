package instances

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/hexagon-codes/hexclaw/adapter"
	"github.com/hexagon-codes/hexclaw/storage"
)

// PhotoRoutes binds the original, authenticated message to a business consumer.
// A legacy route leaves the original K12 receipt and its notice in charge.
type PhotoRoutes struct {
	Prepare         func(context.Context, *adapter.Message, *adapter.Message) (PhotoIntakeRoute, error)
	CommitK12       func(context.Context, *adapter.Message, PhotoIntakeRoute) error
	K12Outcome      func(context.Context, *adapter.Message, PhotoIntakeRoute) (adapter.DeliveryStatus, error)
	RecoverOrdinary func(context.Context, *adapter.Message, PhotoIntakeRoute) (*adapter.Reply, string, error)
	OrdinaryOutcome func(context.Context, *adapter.Message, PhotoIntakeRoute, string) (adapter.DeliveryStatus, error)
}

func (m *Manager) SetPhotoRoutes(routes PhotoRoutes) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.photoRoutes = routes
}

type admittedPhotoKey struct{}
type admittedPhoto struct {
	message        *adapter.Message
	route          PhotoIntakeRoute
	source         [sha256.Size]byte
	instanceID     string
	initialSession string
	delegation     *photoDelegation
}

type photoDelegation struct {
	receiver        *instancePhotoReceiver
	key             PhotoIntakeKey
	mu              sync.Mutex
	ref             string
	partialPrepared bool
}

// NoteDelegatedPhotoReply records an existing delivery owner; assigning a local
// conversation ID in the engine does not alter the original platform message.
func NoteDelegatedPhotoReply(ctx context.Context, msg *adapter.Message, ref string) {
	d := photoDelegationFor(ctx, msg)
	prefix, id, found := strings.Cut(ref, ":")
	if d == nil || !found || id == "" || (prefix != "print" && prefix != "media" && prefix != "batch") {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.ref != "" && d.ref != ref {
		return
	}
	d.ref = ref
	encoded, _ := json.Marshal(ordinaryPhotoOutput{Delegated: true, Reference: ref})
	_, _ = d.receiver.change(context.WithoutCancel(ctx), d.key, "work", "running", PhotoIntakeState{Status: "completed", ResultRef: string(encoded)})
}

func photoDelegationFor(ctx context.Context, msg *adapter.Message) *photoDelegation {
	c, ok := ctx.Value(admittedPhotoKey{}).(admittedPhoto)
	physical, bound := adapter.PlatformInstanceIdentityFromContext(ctx)
	if !ok || !bound || msg == nil || c.message != msg || c.delegation == nil || physical.ID != c.instanceID || physical.Platform != string(msg.Platform) {
		return nil
	}
	origin := *msg
	origin.SessionID = c.initialSession
	if c.source != photoMessageDigest(&origin) {
		return nil
	}
	return c.delegation
}

// AdmittedPhotoRoute recognizes only the original ordinary message and its
// content at the authenticated dispatch boundary.
func AdmittedPhotoRoute(ctx context.Context, msg *adapter.Message) (PhotoIntakeRoute, bool) {
	if ctx == nil || msg == nil {
		return PhotoIntakeRoute{}, false
	}
	c, ok := ctx.Value(admittedPhotoKey{}).(admittedPhoto)
	physical, bound := adapter.PlatformInstanceIdentityFromContext(ctx)
	if !ok || !bound || c.message != msg || msg == nil || physical.ID != c.instanceID || physical.Platform != string(msg.Platform) || c.source != photoMessageDigest(msg) {
		return PhotoIntakeRoute{}, false
	}
	route := c.route
	route.DomainData = append([]byte(nil), route.DomainData...)
	return route, true
}

func photoMessageDigest(msg *adapter.Message) [sha256.Size]byte {
	h := sha256.New()
	write := func(value string) {
		fmt.Fprintf(h, "%d:", len(value))
		h.Write([]byte(value))
	}
	for _, value := range []string{string(msg.Platform), msg.InstanceID, msg.ChatID, msg.UserID, msg.ID, msg.SessionID, msg.Content, msg.ReplyTo, msg.Metadata["request_id"], msg.Metadata["conversation_type"], msg.Metadata["conversation_id"], msg.Metadata["quoted_homework_id"], msg.Metadata["quoted_homework_ambiguous"]} {
		write(value)
	}
	for _, att := range msg.Attachments {
		for _, value := range []string{att.Type, att.ID, att.Mime, att.Name, att.Data, att.URL, att.ContentDigest, strconv.FormatInt(att.Size, 10), strconv.FormatBool(att.Source != nil)} {
			write(value)
		}
	}
	var sum [sha256.Size]byte
	copy(sum[:], h.Sum(nil))
	return sum
}

type instancePhotoReceiver struct {
	manager   *Manager
	inst      Instance
	transport adapter.Adapter
	handler   adapter.MessageHandler
	routes    PhotoRoutes
	store     *PhotoIntakeStore
	ctx       context.Context
	cancel    context.CancelFunc
	mu        sync.Mutex
	workers   sync.WaitGroup
	incoming  map[PhotoIntakeKey]chan struct{}
	active    map[photoLane]bool
}

type photoLane struct {
	key  PhotoIntakeKey
	lane string
}

func newInstancePhotoReceiver(m *Manager, inst *Instance, adp adapter.Adapter, handler adapter.MessageHandler, routes PhotoRoutes) *instancePhotoReceiver {
	ctx, cancel := context.WithCancel(adapter.WithPlatformInstanceIdentity(context.Background(), inst.ID, inst.Provider))
	return &instancePhotoReceiver{manager: m, inst: *inst, transport: adp, handler: handler, routes: routes,
		store: NewPhotoIntakeStore(m.db), ctx: ctx, cancel: cancel, incoming: make(map[PhotoIntakeKey]chan struct{}), active: make(map[photoLane]bool)}
}

func (r *instancePhotoReceiver) stop(ctx context.Context) {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.cancel()
	r.mu.Unlock()
	done := make(chan struct{})
	go func() { r.workers.Wait(); close(done) }()
	select {
	case <-done:
	case <-ctx.Done():
	}
}

func (r *instancePhotoReceiver) enter(ctx context.Context, key PhotoIntakeKey) (func(), error) {
	for {
		r.mu.Lock()
		previous := r.incoming[key]
		if previous == nil {
			ch := make(chan struct{})
			r.incoming[key] = ch
			r.mu.Unlock()
			return func() { r.mu.Lock(); delete(r.incoming, key); close(ch); r.mu.Unlock() }, nil
		}
		r.mu.Unlock()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-r.ctx.Done():
			return nil, r.ctx.Err()
		case <-previous:
		}
	}
}

func (r *instancePhotoReceiver) launch(key PhotoIntakeKey, lane string, run func(context.Context, PhotoIntakeKey) error) {
	id := photoLane{key, lane}
	r.mu.Lock()
	if r.active[id] || r.ctx.Err() != nil {
		r.mu.Unlock()
		return
	}
	r.active[id] = true
	r.workers.Add(1)
	r.mu.Unlock()
	go func() {
		defer r.workers.Done()
		defer func() { r.mu.Lock(); delete(r.active, id); r.mu.Unlock() }()
		delay := time.Second
		for r.ctx.Err() == nil {
			err := run(r.ctx, key)
			if err == nil || r.ctx.Err() != nil || errors.Is(err, sql.ErrNoRows) {
				return
			}
			slog.Warn("IM photo processing paused", "stage", lane, "source_ref", storage.IMRequestIDForEvent(key.Platform, key.InstanceID, key.ProviderMessageID), "error_type", fmt.Sprintf("%T", err))
			if errors.Is(err, ErrPhotoIntakeConflict) || photoWait(r.ctx, delay) != nil {
				return
			}
			delay = min(30*time.Second, delay*2)
		}
	}()
}

func (r *instancePhotoReceiver) ReceivePhotos(ctx context.Context, arrival adapter.PhotoArrival, read adapter.PhotoPageReader) (bool, error) {
	original := arrival.Message
	if original == nil || read == nil || original.UserID == "" || original.ChatID != original.UserID ||
		original.Metadata["conversation_type"] != "1" || arrival.PageCount < 1 {
		return false, nil
	}
	if r.ctx.Err() != nil {
		return true, r.ctx.Err()
	}
	ctx, cancel := context.WithCancel(ctx)
	stopCancel := context.AfterFunc(r.ctx, cancel)
	defer func() { stopCancel(); cancel() }()
	if string(original.Platform) != r.inst.Provider || (original.InstanceID != r.inst.Name && original.InstanceID != r.inst.ID) || original.ID == "" {
		return true, errors.New("IM photo source instance or event identity mismatch")
	}
	msg := *original
	msg.InstanceID = r.inst.ID
	msg.Metadata = maps.Clone(original.Metadata)
	msg.Metadata["channel_instance_name"] = r.inst.Name
	msg.Metadata["channel_instance_name_source"] = "instance_configuration"
	msg.Metadata["request_id"] = storage.IMRequestIDForEvent(r.inst.Provider, r.inst.ID, msg.ID)
	key := PhotoIntakeKey{Platform: r.inst.Provider, InstanceID: r.inst.ID, ChatID: msg.ChatID, ProviderMessageID: msg.ID}
	release, err := r.enter(ctx, key)
	if err != nil {
		return true, err
	}
	defer release()
	receipt, err := r.store.Get(ctx, key)
	missing := errors.Is(err, sql.ErrNoRows)
	if err != nil && !missing {
		return true, err
	}
	route := receipt.Route
	if missing {
		seen, err := r.legacyEvent(ctx, &msg)
		if err != nil || seen {
			return true, err
		}
		route, err = r.routes.Prepare(adapter.WithPlatformInstanceIdentity(ctx, r.inst.ID, r.inst.Provider), original, &msg)
		if err != nil {
			return true, err
		}
		if route.Kind == "legacy" {
			return false, nil
		}
		if route.Kind != "k12" && route.Kind != "ordinary" {
			return true, errors.New("IM photo route is unavailable")
		}
		route.RequestID = msg.Metadata["request_id"]
	}
	signatures, err := photoPageSignatures(arrival)
	if err != nil {
		return true, err
	}
	input := PhotoIntakeInput{Key: key, SenderID: msg.UserID, Content: msg.Content, ReplyTo: msg.ReplyTo, UserName: msg.UserName, OriginalInstanceID: original.InstanceID,
		InstanceName: r.inst.Name, ConversationID: msg.Metadata["conversation_id"], ConversationType: msg.Metadata["conversation_type"],
		QuotedHomework: msg.Metadata["quoted_homework_id"], QuotedHomeworkAmbiguous: msg.Metadata["quoted_homework_ambiguous"] == "true",
		Route: route, PlatformCreatedAtMS: photoMillis(arrival.ProviderCreatedAt), ReceivedAtMS: photoMillis(arrival.ReceivedAt), PageSignatures: signatures}
	var created bool
	receipt, created, err = r.store.Register(ctx, input)
	if err != nil {
		return true, err
	}
	if created {
		ref := sha256.Sum256([]byte(key.ProviderMessageID))
		slog.Info("IM photo intake committed", "source_ref", route.RequestID, "provider_message_ref", hex.EncodeToString(ref[:]), "route", route.Kind, "pages", len(receipt.Pages), "received_at_ms", receipt.ReceivedAtMS, "committed_at_ms", receipt.CommittedAtMS)
	}
	r.launch(key, "notice", r.advanceNotice)
	for i, page := range receipt.Pages {
		if page.StoredAtMS > 0 && page.Signature == signatures[i] {
			continue
		}
		if err = r.store.StartPage(ctx, key, i, time.Now().UnixMilli()); err != nil {
			return true, err
		}
		att, readErr := read(ctx, i)
		if readErr != nil {
			_ = r.store.FailPage(context.WithoutCancel(ctx), key, i, "download_failed", time.Now().UnixMilli())
			return true, readErr
		}
		gotAt := time.Now().UnixMilli()
		if att.Type != "image" || att.Data == "" {
			return true, errors.New("IM photo reader returned no image bytes")
		}
		data, err := base64.StdEncoding.DecodeString(att.Data)
		if err != nil {
			return true, err
		}
		if err = r.store.SavePage(ctx, key, i, signatures[i], att.Name, att.Mime, data, gotAt); err != nil {
			return true, err
		}
	}
	r.launch(key, "work", r.advanceWork)
	return true, nil
}

func photoMillis(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixMilli()
}

func photoPageSignatures(a adapter.PhotoArrival) ([]string, error) {
	digest, err := hex.DecodeString(a.FrameDigest)
	if err != nil || len(digest) != sha256.Size {
		return nil, errors.New("invalid incoming photo fingerprint")
	}
	result := make([]string, a.PageCount)
	for i := range result {
		sum := sha256.Sum256([]byte(a.FrameDigest + ":" + strconv.Itoa(i)))
		result[i] = hex.EncodeToString(sum[:])
	}
	return result, nil
}

func (r *instancePhotoReceiver) legacyEvent(ctx context.Context, msg *adapter.Message) (bool, error) {
	var created time.Time
	err := r.manager.db.QueryRowContext(ctx, "SELECT created_at FROM platform_events WHERE instance_name=? AND event_id=?", r.inst.Name, msg.ID).Scan(&created)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return false, err
	}
	if err == nil && (r.inst.CreatedAt.IsZero() || !created.Before(r.inst.CreatedAt)) {
		var shared bool
		if err = r.manager.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM im_photo_intakes WHERE platform=? AND instance_id=? AND provider_message_id=? AND committed_at_ms<=?)`,
			r.inst.Provider, r.inst.ID, msg.ID, created.UnixMilli()).Scan(&shared); err != nil {
			return false, err
		}
		if !shared {
			return true, nil
		}
	}
	var saved bool
	err = r.manager.db.QueryRowContext(ctx, `SELECT EXISTS(
		SELECT 1 FROM messages m JOIN sessions s ON s.id=m.session_id
		WHERE m.request_id=? AND m.role='assistant' AND s.user_id=? AND s.platform=?
		 AND s.chat_id=? AND s.instance_id IN (?,?))`, msg.Metadata["request_id"], msg.UserID, r.inst.Provider, msg.ChatID, r.inst.ID, r.inst.Name).Scan(&saved)
	return saved, err
}

func (r *instancePhotoReceiver) message(ctx context.Context, receipt PhotoIntakeReceipt, withImages bool) (*adapter.Message, error) {
	k := receipt.Key
	meta := map[string]string{"channel_instance_name": receipt.InstanceName, "channel_instance_name_source": "instance_configuration",
		"conversation_type": receipt.ConversationType, "conversation_id": receipt.ConversationID,
		"request_id": receipt.Route.RequestID, "user_locale": receipt.Route.Locale, "locale": receipt.Route.Locale}
	if receipt.QuotedHomework != "" {
		meta["quoted_homework_id"] = receipt.QuotedHomework
	}
	if receipt.QuotedHomeworkAmbiguous {
		meta["quoted_homework_ambiguous"] = "true"
	}
	msg := &adapter.Message{Platform: adapter.Platform(k.Platform), InstanceID: k.InstanceID, ID: k.ProviderMessageID, ChatID: k.ChatID, UserID: receipt.SenderID,
		UserName: receipt.UserName, Content: receipt.Content, ReplyTo: receipt.ReplyTo, SessionID: receipt.Route.SessionID, Metadata: meta, Timestamp: time.UnixMilli(receipt.ReceivedAtMS)}
	if receipt.Route.Kind == "ordinary" {
		msg.InstanceID = receipt.OriginalInstanceID
		if msg.InstanceID == "" {
			msg.InstanceID = receipt.InstanceName
		}
	}
	if withImages {
		for i := range receipt.Pages {
			p, err := r.store.GetPage(ctx, k, i)
			if err != nil {
				return nil, err
			}
			if len(p.Data) == 0 {
				return nil, errors.New("pending IM image bytes are unavailable")
			}
			msg.Attachments = append(msg.Attachments, adapter.Attachment{Type: "image", Name: p.Name, Mime: p.MIME, Size: int64(len(p.Data)), ContentDigest: p.Digest, Data: base64.StdEncoding.EncodeToString(p.Data)})
		}
	}
	return msg, nil
}

// RecoverPhotos schedules only rows belonging to the attached physical instances.
func (m *Manager) RecoverPhotos(ctx context.Context) error {
	m.mu.Lock()
	m.photoRecoveryEnabled = true
	receivers := make([]*instancePhotoReceiver, 0, len(m.photoReceivers))
	for _, r := range m.photoReceivers {
		receivers = append(receivers, r)
	}
	m.mu.Unlock()
	var failures []error
	for _, r := range receivers {
		failures = append(failures, r.recover(ctx))
	}
	return errors.Join(failures...)
}

func (r *instancePhotoReceiver) resumeAttached() {
	r.launch(PhotoIntakeKey{Platform: r.inst.Provider, InstanceID: r.inst.ID}, "recovery", func(ctx context.Context, _ PhotoIntakeKey) error { return r.recover(ctx) })
}

func (r *instancePhotoReceiver) recover(ctx context.Context) error {
	var after string
	for {
		rows, err := r.store.Recoverable(ctx, r.inst.ID, after, 100)
		if err != nil {
			return err
		}
		for _, row := range rows {
			r.launch(row.Key, "notice", r.advanceNotice)
			if row.AssetsReadyAtMS > 0 {
				r.launch(row.Key, "work", r.advanceWork)
			}
			after = row.ID
		}
		if len(rows) < 100 {
			return nil
		}
	}
}
