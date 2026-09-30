package acp

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"time"

	acpruntime "github.com/uvwt/agentdock/internal/acp"
)

func (s *Service) Session(ctx context.Context, request SessionRequest) (response Result, returnErr error) {
	manager, profileID, err := s.managerFor(request.ProfileID)
	if err != nil {
		return nil, err
	}
	defer func() {
		if response != nil && returnErr == nil {
			response["profile_id"] = profileID
		}
	}()

	action := actionArg(request.Action)
	if !isSessionAction(action) {
		return nil, validationError("ACP_ACTION_INVALID", "unsupported ACP session action", map[string]any{"action": action})
	}
	if action != "info" && strings.TrimSpace(request.AuthMethodID) != "" {
		if err := manager.Authenticate(ctx, request.AuthMethodID); err != nil {
			return nil, acpToolError(err)
		}
	}
	switch action {
	case "info":
		authMethodID := strings.TrimSpace(request.AuthMethodID)
		if authMethodID != "" {
			if err := manager.Authenticate(ctx, authMethodID); err != nil {
				return nil, acpToolError(err)
			}
		}
		info, err := manager.AgentInfo(ctx)
		if err != nil {
			return nil, acpToolError(err)
		}
		policies := acpruntime.CurrentPolicies()
		result := Result{
			"action": action, "agent": info.AgentInfo, "capabilities": info.AgentCapabilities,
			"protocol_version": info.ProtocolVersion, "auth_methods": append([]any{}, info.AuthMethods...),
			"context_policy": policies.Context, "event_policy": policies.Events,
			"interaction_policy": policies.Interactions, "steering_policy": policies.Steering,
		}
		if authMethodID != "" {
			result["auth_method_id"] = authMethodID
			result["authenticated"] = true
		}
		return result, nil

	case "new":
		var result acpruntime.SessionResult
		if strings.TrimSpace(request.FromSessionID) != "" {
			additional := request.AdditionalDirectories
			result, err = manager.ForkSession(ctx, request.FromSessionID, request.CWD, additional)
		} else {
			result, err = manager.NewSession(ctx, request.CWD, request.AdditionalDirectories)
		}
		if err != nil {
			return nil, acpToolError(err)
		}
		return sessionActionResult(action, result), nil

	case "list":
		managed, err := manager.ListManagedSessions()
		if err != nil {
			return nil, acpToolError(err)
		}
		page, err := manager.ListRemoteSessions(ctx, request.CWD, request.Cursor)
		if err != nil {
			var acpErr *acpruntime.Error
			if errors.As(err, &acpErr) && acpErr.Code == "ACP_CAPABILITY_UNSUPPORTED" {
				sessions := mergeSessionList(profileID, managed, nil)
				return Result{
					"action": action, "sessions": sessions, "count": len(sessions),
					"managed_count": len(managed), "remote_count": 0, "remote_available": false,
					"remote_error": map[string]any{"code": acpErr.Code, "message": acpErr.Message},
				}, nil
			}
			return nil, acpToolError(err)
		}
		sessions := mergeSessionList(profileID, managed, page.Sessions)
		result := Result{
			"action": action, "sessions": sessions, "count": len(sessions),
			"managed_count": len(managed), "remote_count": len(page.Sessions), "remote_available": true,
		}
		if page.NextCursor != "" {
			result["next_cursor"] = page.NextCursor
		}
		return result, nil

	case "inspect":
		if err := requireOneSessionTarget(request.SessionID, request.RemoteSessionID); err != nil {
			return nil, err
		}
		result := Result{"action": action}
		if strings.TrimSpace(request.SessionID) != "" {
			session, err := manager.InspectSession(request.SessionID)
			if err != nil {
				return nil, acpToolError(err)
			}
			enrichSessionTitle(ctx, manager, &session)
			result["session"] = session
			projection, projectionErr := manager.SessionProjection(session.ID)
			if projectionErr == nil {
				result["runtime_state"] = projection
			}
			if boolValue(request.IncludeHistory, false) {
				history, _, err := manager.ReadRemoteHistory(ctx, session.RemoteSessionID, session.CWD, session.AdditionalDirectories)
				if err != nil {
					return nil, acpToolError(err)
				}
				appendHistoryResult(result, history)
			}
			return result, nil
		}

		remote, err := manager.FindRemoteSession(ctx, request.RemoteSessionID)
		if err != nil {
			return nil, acpToolError(err)
		}
		result["remote_session"] = remote
		if boolValue(request.IncludeHistory, false) {
			history, _, err := manager.ReadRemoteHistory(ctx, remote.RemoteSessionID, remote.CWD, remote.AdditionalDirectories)
			if err != nil {
				return nil, acpToolError(err)
			}
			appendHistoryResult(result, history)
		}
		return result, nil

	case "open":
		result, attached, err := manager.OpenSession(ctx, request.SessionID, request.RemoteSessionID)
		if err != nil {
			return nil, acpToolError(err)
		}
		enrichSessionTitle(ctx, manager, &result.Session)
		response := sessionActionResult(action, result)
		response["attached"] = attached
		return response, nil

	case "update":
		if strings.TrimSpace(request.SessionID) == "" {
			return nil, validationError("ACP_SESSION_TARGET_REQUIRED", "session_id is required for update", nil)
		}
		modeID := strings.TrimSpace(request.ModeID)
		configID := strings.TrimSpace(request.ConfigID)
		hasMode := modeID != ""
		hasConfig := configID != ""
		if hasMode == hasConfig {
			return nil, validationError("ACP_SESSION_UPDATE_INVALID", "provide exactly one of mode_id or config_id for update", nil)
		}
		current, err := manager.EnsureSessionActive(ctx, request.SessionID)
		if err != nil {
			return nil, acpToolError(err)
		}
		if hasMode {
			before := acpCurrentMode(current.Modes, current.Session.ModeID)
			if err := manager.SetSessionMode(ctx, request.SessionID, modeID); err != nil {
				return nil, acpToolError(err)
			}
			updated, err := manager.EnsureSessionActive(ctx, request.SessionID)
			if err != nil {
				return nil, acpToolError(err)
			}
			enrichSessionTitle(ctx, manager, &updated.Session)
			result := sessionActionResult(action, updated)
			after := acpCurrentMode(updated.Modes, updated.Session.ModeID)
			result["change"] = acpSettingChange("mode", "mode", "Mode", before, after)
			if before != "" && after != "" {
				result["changed"] = before != after
			}
			return result, nil
		} else {
			label, before, beforeFound := acpConfigOption(current.ConfigOptions, configID)
			if _, err := manager.SetSessionConfigOption(ctx, request.SessionID, configID, request.ConfigValue); err != nil {
				return nil, acpToolError(err)
			}
			updated, err := manager.EnsureSessionActive(ctx, request.SessionID)
			if err != nil {
				return nil, acpToolError(err)
			}
			enrichSessionTitle(ctx, manager, &updated.Session)
			result := sessionActionResult(action, updated)
			afterLabel, after, afterFound := acpConfigOption(updated.ConfigOptions, configID)
			if afterLabel != "" {
				label = afterLabel
			}
			if label == "" {
				label = configID
			}
			change := acpSettingChange("config_option", configID, label, before, after)
			if !beforeFound {
				delete(change, "before")
			}
			if !afterFound {
				change["after"] = request.ConfigValue
			}
			result["change"] = change
			if beforeFound && afterFound {
				result["changed"] = !reflect.DeepEqual(before, after)
			}
			return result, nil
		}

	case "close":
		if strings.TrimSpace(request.SessionID) == "" {
			return nil, validationError("ACP_SESSION_TARGET_REQUIRED", "session_id is required for close", nil)
		}
		before, err := manager.InspectSession(request.SessionID)
		if err != nil {
			return nil, acpToolError(err)
		}
		enrichSessionTitle(ctx, manager, &before)
		session, err := manager.CloseSession(ctx, request.SessionID)
		if err != nil {
			return nil, acpToolError(err)
		}
		if session.Title == "" {
			session.Title = before.Title
		}
		return Result{"action": action, "session": session}, nil

	case "delete":
		if err := requireOneSessionTarget(request.SessionID, request.RemoteSessionID); err != nil {
			return nil, err
		}
		title := ""
		if strings.TrimSpace(request.SessionID) != "" {
			session, inspectErr := manager.InspectSession(request.SessionID)
			if inspectErr != nil {
				return nil, acpToolError(inspectErr)
			}
			enrichSessionTitle(ctx, manager, &session)
			title = session.Title
			err = manager.DeleteSession(ctx, request.SessionID)
		} else {
			if remote, findErr := manager.FindRemoteSession(ctx, request.RemoteSessionID); findErr == nil {
				title = strings.TrimSpace(remote.Title)
			}
			err = manager.DeleteRemoteSession(ctx, request.RemoteSessionID)
		}
		if err != nil {
			return nil, acpToolError(err)
		}
		result := Result{"action": action, "deleted": true}
		if title != "" {
			result["title"] = title
		}
		return result, nil

	default:
		return nil, validationError("ACP_ACTION_INVALID", "unsupported ACP session action", map[string]any{"action": action})
	}
}

func enrichSessionTitle(ctx context.Context, manager *acpruntime.Manager, session *acpruntime.SessionRecord) {
	if session == nil || strings.TrimSpace(session.Title) != "" || strings.TrimSpace(session.RemoteSessionID) == "" {
		return
	}
	remote, err := manager.FindRemoteSession(ctx, session.RemoteSessionID)
	if err != nil {
		return
	}
	session.Title = strings.TrimSpace(remote.Title)
}

func acpSettingChange(field, id, label string, before, after any) map[string]any {
	return map[string]any{
		"field":  field,
		"id":     id,
		"label":  label,
		"before": before,
		"after":  after,
	}
}

func acpCurrentMode(modes any, fallback string) string {
	if state, ok := modes.(map[string]any); ok {
		if modeID, _ := state["currentModeId"].(string); strings.TrimSpace(modeID) != "" {
			return strings.TrimSpace(modeID)
		}
		if modeID, _ := state["current_mode_id"].(string); strings.TrimSpace(modeID) != "" {
			return strings.TrimSpace(modeID)
		}
	}
	return strings.TrimSpace(fallback)
}

func acpConfigOption(options any, configID string) (string, any, bool) {
	read := func(option map[string]any) (string, any, bool) {
		id, _ := option["id"].(string)
		if id == "" {
			id, _ = option["optionId"].(string)
		}
		if id == "" {
			id, _ = option["option_id"].(string)
		}
		if id != configID {
			return "", nil, false
		}
		label, _ := option["name"].(string)
		if label == "" {
			label, _ = option["title"].(string)
		}
		value, exists := option["currentValue"]
		if !exists {
			value, exists = option["current_value"]
		}
		return label, value, exists
	}

	switch typed := options.(type) {
	case []any:
		for _, raw := range typed {
			if option, ok := raw.(map[string]any); ok {
				if label, value, found := read(option); found {
					return label, value, true
				}
			}
		}
	case []map[string]any:
		for _, option := range typed {
			if label, value, found := read(option); found {
				return label, value, true
			}
		}
	}
	return "", nil, false
}

func sessionActionResult(action string, result acpruntime.SessionResult) Result {
	response := Result{"action": action, "session": result.Session, "agent": result.Agent}
	if result.Modes != nil {
		response["modes"] = result.Modes
	}
	if result.ConfigOptions != nil {
		response["config_options"] = result.ConfigOptions
	}
	return response
}

func (s *Service) Prompt(ctx context.Context, request PromptRequest) (response Result, returnErr error) {
	manager, profileID, err := s.managerFor(request.ProfileID)
	if err != nil {
		return nil, err
	}
	defer func() {
		if response != nil && returnErr == nil {
			response["profile_id"] = profileID
		}
	}()
	action := actionArg(request.Action)
	switch action {
	case "start":
		blocks := make([]acpruntime.ContentBlock, 0, len(request.Prompt))
		for _, block := range request.Prompt {
			blocks = append(blocks, acpruntime.ContentBlock(block))
		}
		result, err := manager.SubmitPrompt(ctx, request.SessionID, blocks)
		if err != nil {
			return nil, acpToolError(err)
		}
		response := Result{
			"action": action, "run_id": result.RunID, "session_id": result.SessionID,
			"status": result.Status, "disposition": result.Disposition, "started_at": result.StartedAt,
		}
		if title := localSessionTitle(manager, result.SessionID); title != "" {
			response["title"] = title
		}
		return response, nil
	case "events":
		after := intValue(request.AfterSeq, 0)
		if after < 0 {
			return nil, validationError("ACP_AFTER_SEQ_INVALID", "after_seq must not be negative", map[string]any{"after_seq": after})
		}
		limit := intValue(request.Limit, 100)
		waitMS := intValue(request.WaitMS, 0)
		if waitMS < 0 {
			return nil, validationError("ACP_WAIT_INVALID", "wait_ms must not be negative", map[string]any{"wait_ms": waitMS})
		}
		if waitMS > 25000 {
			waitMS = 25000
		}
		result, err := manager.PromptEvents(ctx, request.RunID, uint64(after), limit, time.Duration(waitMS)*time.Millisecond)
		if err != nil {
			return nil, acpToolError(err)
		}
		response := Result{
			"action": action, "run_id": result.RunID, "session_id": result.SessionID,
			"status": result.Status, "events": result.Events, "next_seq": result.NextSeq,
			"first_seq": result.FirstSeq, "latest_seq": result.LatestSeq,
			"dropped_count": result.DroppedCount, "has_more": result.HasMore, "truncated": result.Truncated,
			"started_at": result.StartedAt, "cancel_requested": result.CancelRequested,
			"stop_reason": result.StopReason, "error_code": result.ErrorCode, "message": result.Message,
		}
		if result.EndedAt != nil {
			response["ended_at"] = result.EndedAt
		}
		if title := localSessionTitle(manager, result.SessionID); title != "" {
			response["title"] = title
		}
		return response, nil
	case "cancel":
		sessionID := request.SessionID
		runID := request.RunID
		if sessionID == "" && runID == "" {
			return nil, validationError("ACP_CANCEL_TARGET_REQUIRED", "session_id or run_id is required for cancel", nil)
		}
		if err := manager.CancelPrompt(ctx, sessionID, runID); err != nil {
			return nil, acpToolError(err)
		}
		response := Result{"action": action, "session_id": sessionID, "run_id": runID, "cancel_requested": true}
		if title := localSessionTitle(manager, sessionID); title != "" {
			response["title"] = title
		}
		return response, nil
	default:
		return nil, validationError("ACP_ACTION_INVALID", "unsupported ACP prompt action", map[string]any{"action": action})
	}
}

func localSessionTitle(manager *acpruntime.Manager, sessionID string) string {
	if manager == nil || strings.TrimSpace(sessionID) == "" {
		return ""
	}
	session, err := manager.InspectSession(sessionID)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(session.Title)
}

func (s *Service) Interaction(_ context.Context, request InteractionRequest) (response Result, returnErr error) {
	manager, profileID, err := s.managerFor(request.ProfileID)
	if err != nil {
		return nil, err
	}
	defer func() {
		if response != nil && returnErr == nil {
			response["profile_id"] = profileID
		}
	}()

	action := actionArg(request.Action)
	switch action {
	case "list":
		interactions := manager.ListInteractions(request.SessionID, boolValue(request.PendingOnly, true))
		return Result{"action": action, "interactions": interactions, "count": len(interactions)}, nil

	case "respond":
		if strings.TrimSpace(request.InteractionID) == "" {
			return nil, validationError("ACP_INTERACTION_ID_REQUIRED", "interaction_id is required for respond", nil)
		}
		responseAction := actionArg(request.Response.Action)
		optionID := strings.TrimSpace(request.Response.OptionID)
		cancelled := responseAction == "cancel"
		if responseAction != "" && !cancelled {
			return nil, validationError("ACP_INTERACTION_RESPONSE_INVALID", "unsupported interaction response action", map[string]any{"action": responseAction})
		}
		if cancelled == (optionID != "") {
			return nil, validationError("ACP_INTERACTION_RESPONSE_INVALID", "provide exactly one of response.option_id or response.action=cancel", nil)
		}
		interaction, err := manager.RespondInteraction(request.InteractionID, optionID, cancelled)
		if err != nil {
			return nil, acpToolError(err)
		}
		return Result{"action": action, "interaction": interaction, "responded": true}, nil

	default:
		return nil, validationError("ACP_ACTION_INVALID", "unsupported ACP interaction action", map[string]any{"action": action})
	}
}
