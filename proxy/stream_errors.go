package proxy

import (
	"fmt"
	"io"
	"strings"
)

type kiroStreamHeaders struct {
	eventType, messageType, exceptionType, errorCode, errorMessage string
}

// Decode all routing/error headers, including exceptions without an event type.
func parseKiroStreamHeaders(data []byte) (h kiroStreamHeaders, err error) {
	widths := [...]int{0, 0, 1, 2, 4, 8, -1, -1, 8, 16}
	for offset := 0; offset < len(data); {
		nameLen := int(data[offset])
		offset++
		if offset+nameLen >= len(data) {
			return h, io.ErrUnexpectedEOF
		}
		name := string(data[offset : offset+nameLen])
		offset += nameLen
		kind := int(data[offset])
		offset++
		if kind >= len(widths) {
			return h, fmt.Errorf("invalid Kiro stream header type %d", kind)
		}
		size := widths[kind]
		if size < 0 {
			if offset+2 > len(data) {
				return h, io.ErrUnexpectedEOF
			}
			size = int(data[offset])<<8 | int(data[offset+1])
			offset += 2
		}
		if offset+size > len(data) {
			return h, io.ErrUnexpectedEOF
		}
		if kind == 7 {
			value := string(data[offset : offset+size])
			switch name {
			case ":event-type":
				h.eventType = value
			case ":message-type":
				h.messageType = value
			case ":exception-type":
				h.exceptionType = value
			case ":error-code":
				h.errorCode = value
			case ":error-message":
				h.errorMessage = value
			}
		}
		offset += size
	}
	return h, nil
}

type kiroStreamError struct {
	code, message string
	status        int
}

func (e *kiroStreamError) Error() string {
	if e.status != 0 {
		return fmt.Sprintf("HTTP %d from Kiro stream (%s): %s", e.status, e.code, e.message)
	}
	return fmt.Sprintf("Kiro stream error (%s): %s", e.code, e.message)
}

func parseKiroStreamError(h kiroStreamHeaders, event map[string]interface{}, raw []byte) error {
	code := h.exceptionType
	if code == "" {
		code = h.errorCode
	}
	if code == "" && (strings.HasSuffix(strings.ToLower(h.eventType), "exception") || h.eventType == "error") {
		code = h.eventType
	}
	isError := code != "" || h.messageType == "exception" || h.messageType == "error"
	if nested, ok := event["error"]; ok && nested != nil {
		isError = true
		if detail, ok := nested.(map[string]interface{}); ok {
			event = detail
		} else if message, ok := nested.(string); ok {
			h.errorMessage = message
		}
	}
	for name, value := range event {
		if strings.HasSuffix(strings.ToLower(name), "exception") {
			isError, code = true, name
			if detail, ok := value.(map[string]interface{}); ok {
				event = detail
			}
			break
		}
	}
	if !isError {
		return nil
	}
	if code == "" {
		code = firstStringField(event, "code", "type", "__type")
	}
	if code == "" {
		code = "unknown"
	}
	message := firstStringField(event, "message", "Message", "errorMessage", "originalMessage", "reason")
	if message == "" {
		message = h.errorMessage
	}
	if message == "" {
		message = strings.TrimSpace(string(raw))
	}
	if isContextLengthError(message) {
		return errContextTooLong
	}
	status, _ := readTokenNumber(event, "statusCode", "status_code", "httpStatusCode", "originalStatusCode")
	if status < 400 || status > 599 {
		status = 0
		shortCode := code[strings.LastIndexAny(code, "#:")+1:]
		switch strings.ToLower(shortCode) {
		case "validationexception", "invalidrequestexception":
			status = 400
		case "unauthorizedexception", "unauthenticatedexception":
			status = 401
		case "accessdeniedexception":
			status = 403
		case "resourcenotfoundexception":
			status = 404
		case "modeltimeoutexception":
			status = 408
		case "modelstreamerrorexception":
			status = 424
		case "throttlingexception", "servicequotaexceededexception", "modelnotreadyexception":
			status = 429
		case "internalserverexception", "internalservererror":
			status = 500
		case "serviceunavailableexception":
			status = 503
		}
	}
	return &kiroStreamError{code: code, message: message, status: status}
}
