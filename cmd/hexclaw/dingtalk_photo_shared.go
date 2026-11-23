package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/hexagon-codes/hexclaw/adapter"
	"github.com/hexagon-codes/hexclaw/instances"
	"github.com/hexagon-codes/hexclaw/records"
	k12 "github.com/hexagon-codes/hexclaw/scenarios/k12"
	k12usecase "github.com/hexagon-codes/hexclaw/scenarios/k12/usecase"
)

const k12DingtalkSharedPhotoStatusNoticeVersion = "shared_v1"

func k12DingtalkAdmissionPages(attachments []adapter.Attachment) ([]k12usecase.InboundPhotoAdmission, error) {
	if len(attachments) == 0 {
		return nil, errors.New("DingTalk photo admission requires an image")
	}
	pages := make([]k12usecase.InboundPhotoAdmission, len(attachments))
	for i, attachment := range attachments {
		if strings.TrimSpace(attachment.Type) != "image" {
			return nil, errors.New("DingTalk photo admission contains a non-image attachment")
		}
		data, err := decodeK12PhotoAttachment(attachment)
		if err != nil {
			return nil, err
		}
		name := strings.TrimSpace(attachment.Name)
		if name == "" {
			name = "dingtalk-picture"
		}
		pages[i] = k12usecase.InboundPhotoAdmission{AssetName: name, AssetMIME: strings.ToLower(strings.TrimSpace(attachment.Mime)), AssetBytes: data}
	}
	return pages, nil
}

func k12PhotoReceiptForRoute(identity k12usecase.InboundPhotoIdentity, route instances.PhotoIntakeRoute) k12usecase.InboundPhotoReceipt {
	return k12usecase.InboundPhotoReceipt{
		OwnerScope: route.OwnerScope, AgentName: route.AgentName, BindingID: route.BindingID,
		Identity: identity, CommandJSON: string(route.DomainData),
	}
}

func sameDingtalkPhotoSource(original, canonical *adapter.Message) bool {
	return original != nil && canonical != nil && original.Platform == adapter.PlatformDingtalk && original.Platform == canonical.Platform &&
		original.ID == canonical.ID && original.ChatID == canonical.ChatID && original.UserID == canonical.UserID &&
		sharedDingtalkDirectPhoto(original) && sharedDingtalkDirectPhoto(canonical)
}

func sharedDingtalkDirectPhoto(msg *adapter.Message) bool {
	switch strings.ToLower(strings.TrimSpace(msg.Metadata["conversation_type"])) {
	case "", "1", "direct":
		return true
	default:
		return false
	}
}

// LookupInboundPhotoReceipt 仅读首次归属。别名不能将同名实例的旧数据带到另一物理来源。
func (r *k12DingtalkPhotoInboundRuntime) LookupInboundPhotoReceipt(ctx context.Context, original, canonical *adapter.Message) (k12usecase.InboundPhotoReceipt, bool, error) {
	var receipt k12usecase.InboundPhotoReceipt
	if r == nil || r.inbound == nil || !sameDingtalkPhotoSource(original, canonical) {
		return receipt, false, nil
	}
	stable, err := k12DingtalkInboundIdentity(canonical)
	if err != nil {
		return receipt, false, err
	}
	found, err := r.inbound.ResumeByIdentity(ctx, stable)
	if err == nil {
		return found.Receipt, true, nil
	}
	if !errors.Is(err, records.ErrNotFound) {
		return receipt, false, err
	}
	alias, err := k12DingtalkInboundIdentity(original)
	if err != nil {
		return receipt, false, err
	}
	if alias == stable || r.resolveInstanceID == nil {
		return receipt, false, nil
	}
	found, err = r.inbound.ResumeByIdentity(ctx, alias)
	if errors.Is(err, records.ErrNotFound) {
		return receipt, false, nil
	}
	if err != nil {
		return receipt, false, err
	}
	resolved, err := r.resolveInstanceID(alias.Platform, alias.InstanceID)
	if err != nil {
		return receipt, false, fmt.Errorf("verify historical DingTalk photo instance: %w", err)
	}
	if resolved != stable.InstanceID {
		return receipt, false, nil
	}
	return found.Receipt, true, nil
}

// PrepareSharedPhoto 仅在共享接单完成来源检查后冻结业务命令，收图前无需读取附件。
func (r *k12DingtalkPhotoInboundRuntime) PrepareSharedPhoto(ctx context.Context, original, canonical *adapter.Message) (instances.PhotoIntakeRoute, bool, error) {
	if r == nil || r.inbound == nil || !sameDingtalkPhotoSource(original, canonical) {
		return instances.PhotoIntakeRoute{}, false, nil
	}
	if _, err := k12DingtalkInboundIdentity(canonical); err != nil {
		return instances.PhotoIntakeRoute{}, false, err
	}
	return r.prepareDingtalkPhotoRoute(ctx, original, canonical, k12DingtalkSharedPhotoStatusNoticeVersion, nil)
}

func (r *k12DingtalkPhotoInboundRuntime) prepareDingtalkPhotoRoute(ctx context.Context, originalMessage, msg *adapter.Message, noticeVersion string, check func(context.Context, *adapter.Message) error) (instances.PhotoIntakeRoute, bool, error) {
	var route instances.PhotoIntakeRoute
	routed, routingMessage := lookupK12DingtalkTutorRoute(msg, originalMessage, r.router)
	if routed == nil {
		return route, false, nil
	}
	if noticeVersion == k12DingtalkSharedPhotoStatusNoticeVersion && routingMessage.InstanceID != msg.InstanceID {
		if r.resolveInstanceID == nil {
			return route, false, nil
		}
		stable, err := r.resolveInstanceID(string(msg.Platform), routingMessage.InstanceID)
		if err != nil {
			return route, false, fmt.Errorf("verify legacy DingTalk photo route: %w", err)
		}
		if stable != msg.InstanceID {
			return route, false, nil
		}
	}
	if check != nil {
		if err := check(ctx, msg); err != nil {
			return route, false, err
		}
	}
	if r.bindDirect != nil {
		if err := r.bindDirect(ctx, routingMessage); err != nil {
			return route, false, fmt.Errorf("bind DingTalk direct K12 target: %w", err)
		}
		rebound, _ := lookupK12DingtalkTutorRoute(msg, originalMessage, r.router)
		if rebound == nil || rebound.Rule == nil || strings.TrimSpace(rebound.Rule.ChatID) == "" ||
			(noticeVersion == k12DingtalkSharedPhotoStatusNoticeVersion && (rebound.AgentName != routed.AgentName || strings.TrimSpace(rebound.Rule.ChatID) != strings.TrimSpace(msg.ChatID))) {
			return route, false, errors.New("DingTalk direct K12 target binding did not produce an exact route")
		}
		routed = rebound
	}
	provider, model, exact := normalizeK12DingtalkInboundPhotoRoute(routed.AgentConfig.Provider, routed.AgentConfig.Model)
	resolve := r.resolveRoute
	if noticeVersion == k12DingtalkSharedPhotoStatusNoticeVersion {
		// 首提示只需模型名称。处于同一框架的实际能力探测仍由 ImageTask 在取图后执行。
		resolve = r.resolveSharedRoute
	}
	if !exact && resolve != nil {
		resolved, err := resolve(k12.ImageTaskRouteSnapshot{Provider: provider, Model: model})
		if err != nil {
			return route, false, fmt.Errorf("resolve DingTalk inbound photo route: %w", err)
		}
		provider, model, exact = normalizeK12DingtalkInboundPhotoRoute(resolved.Provider, resolved.Model)
	}
	if !exact {
		return route, false, errors.New("DingTalk inbound TutorAgent route is incomplete")
	}
	locale := ""
	if noticeVersion == k12DingtalkSharedPhotoStatusNoticeVersion {
		locale = msg.Metadata["locale"]
	} else if r.messageLocale != nil {
		locale = r.messageLocale()
	}
	sourceSession := k12PhotoSourceSession(msg)
	command := k12DingtalkInboundPhotoCommand{
		StatusNoticeVersion: noticeVersion, SchemaVersion: 2, MessageLocale: locale,
		SourceSessionID: sourceSession, MessageIntent: strings.TrimSpace(msg.Content), Provider: provider, Model: model,
	}
	raw, err := json.Marshal(command)
	if err != nil {
		return route, false, fmt.Errorf("encode DingTalk inbound photo command: %w", err)
	}
	return instances.PhotoIntakeRoute{
		Kind: "k12", OwnerScope: k12usecase.DefaultLocalOwnerScope, AgentName: routed.AgentName, BindingID: stableBindingID(*routed.Rule),
		SessionID: sourceSession, Locale: locale, Provider: provider, Model: model, DomainData: raw,
	}, true, nil
}

func k12SharedPhotoReceipt(msg *adapter.Message, route instances.PhotoIntakeRoute) (k12usecase.InboundPhotoReceipt, error) {
	identity, err := k12DingtalkInboundIdentity(msg)
	if err != nil {
		return k12usecase.InboundPhotoReceipt{}, err
	}
	var command k12DingtalkInboundPhotoCommand
	provider, model, exact := normalizeK12DingtalkInboundPhotoRoute(route.Provider, route.Model)
	if !sharedDingtalkDirectPhoto(msg) || route.Kind != "k12" || strings.TrimSpace(route.OwnerScope) == "" ||
		strings.TrimSpace(route.AgentName) == "" || strings.TrimSpace(route.BindingID) == "" || strings.TrimSpace(route.SessionID) == "" || !exact ||
		json.Unmarshal(route.DomainData, &command) != nil || command.SchemaVersion != 2 || command.StatusNoticeVersion != k12DingtalkSharedPhotoStatusNoticeVersion ||
		command.SourceSessionID != route.SessionID || command.MessageLocale != route.Locale || command.MessageIntent != strings.TrimSpace(msg.Content) ||
		command.Provider != provider || command.Model != model {
		return k12usecase.InboundPhotoReceipt{}, errors.New("frozen DingTalk K12 photo command is inconsistent")
	}
	return k12PhotoReceiptForRoute(identity, route), nil
}

// AdmitSharedPhoto 使用共享层按原消息保存的有序图片；重复提交由原 K12 事务核对摘要、页数及目标。
func (r *k12DingtalkPhotoInboundRuntime) AdmitSharedPhoto(ctx context.Context, msg *adapter.Message, route instances.PhotoIntakeRoute) error {
	if r == nil || r.inbound == nil {
		return errors.New("DingTalk inbound photo runtime is unavailable")
	}
	receipt, err := k12SharedPhotoReceipt(msg, route)
	if err != nil {
		return err
	}
	pages, err := k12DingtalkAdmissionPages(msg.Attachments)
	if err != nil {
		return err
	}
	_, err = r.admitPhotoPages(ctx, pages, receipt, receipt.Identity.InstanceID, time.Now())
	return err
}

// SharedPhotoOutcome 回读原接单的所有页。等待、结果未知和未确认投递都不能使首提示提前撤回。
func (r *k12DingtalkPhotoInboundRuntime) SharedPhotoOutcome(ctx context.Context, msg *adapter.Message, route instances.PhotoIntakeRoute) (adapter.DeliveryStatus, error) {
	if r == nil || r.inbound == nil {
		return "", errors.New("DingTalk inbound photo runtime is unavailable")
	}
	expected, err := k12SharedPhotoReceipt(msg, route)
	if err != nil {
		return "", err
	}
	root, err := r.inbound.ResumeByIdentity(ctx, expected.Identity)
	if errors.Is(err, records.ErrNotFound) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	receipt := root.Receipt
	match := func(page k12usecase.InboundPhotoBundle, key k12usecase.InboundPhotoIdentity) bool {
		return page.Receipt.Identity == key && page.Receipt.AgentName == expected.AgentName && page.Receipt.BindingID == expected.BindingID &&
			page.Receipt.OwnerScope == expected.OwnerScope && page.Receipt.CommandJSON == expected.CommandJSON && page.Receipt.PageCount == receipt.PageCount
	}
	if !match(root, expected.Identity) || receipt.PageCount < 1 {
		return "", errors.New("DingTalk K12 receipt does not match its frozen photo route")
	}
	outcome := k12PhotoPageOutcome(root.Dispatch)
	if outcome == "" || receipt.PageCount == 1 {
		return outcome, nil
	}
	reader, ok := r.inbound.(interface {
		PendingEarlierPages(context.Context, k12usecase.InboundPhotoIdentity) ([]k12usecase.InboundPhotoDispatch, error)
	})
	if !ok {
		return "", errors.New("DingTalk photo page status is unavailable")
	}
	last := expected.Identity
	last.PageIndex = receipt.PageCount
	pending, err := reader.PendingEarlierPages(ctx, last)
	if err != nil || len(pending) != 0 {
		return "", err
	}
	// 恢复后的缺页也不能当成送达。仅在没有待处理页时核对最终成员与每页结论。
	for i := 1; i < receipt.PageCount; i++ {
		key := receipt.Identity
		key.PageIndex = i
		page, err := r.inbound.ResumeByIdentity(ctx, key)
		if err != nil {
			return "", err
		}
		if !match(page, key) {
			return "", errors.New("DingTalk K12 page does not match its frozen source")
		}
		switch k12PhotoPageOutcome(page.Dispatch) {
		case "":
			return "", nil
		case adapter.DeliveryFailed:
			outcome = adapter.DeliveryFailed
		}
	}
	return outcome, nil
}

func k12PhotoPageOutcome(dispatch k12usecase.InboundPhotoDispatch) adapter.DeliveryStatus {
	switch {
	case dispatch.TerminalStatus == k12usecase.InboundPhotoTerminalFailed:
		return adapter.DeliveryFailed
	case dispatch.ReplyStatus == k12usecase.InboundPhotoReplyDelivered:
		return adapter.DeliveryDelivered
	default:
		return ""
	}
}
