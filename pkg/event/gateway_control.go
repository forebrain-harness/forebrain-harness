package event

import (
	"encoding/json"
	"strings"
)

const (
	GatewayControlTypeRequest   = "control_request"
	GatewayControlTypeResponse  = "control_response"
	GatewayControlTypeCancel    = "control_cancel_request"
	GatewayControlTypeKeepAlive = "keep_alive"
)

type GatewayControlRequest struct {
	Type      string                `json:"type"`
	RequestID string                `json:"request_id"`
	Request   GatewayControlPayload `json:"request"`
}

type GatewayControlPayload struct {
	Subtype  string          `json:"subtype"`
	ToolName string          `json:"tool_name,omitempty"`
	Input    json.RawMessage `json:"input,omitempty"`
}

type GatewayControlResponse struct {
	Type     string                     `json:"type"`
	Response GatewayControlResponseBody `json:"response"`
}

type GatewayControlResponseBody struct {
	Subtype   string         `json:"subtype"`
	RequestID string         `json:"request_id"`
	Response  map[string]any `json:"response,omitempty"`
	Error     string         `json:"error,omitempty"`
}

func NewGatewayControlSuccessResponse(requestID string, response map[string]any) GatewayControlResponse {
	return GatewayControlResponse{
		Type: GatewayControlTypeResponse,
		Response: GatewayControlResponseBody{
			Subtype:   "success",
			RequestID: strings.TrimSpace(requestID),
			Response:  response,
		},
	}
}

func NewGatewayControlErrorResponse(requestID string, message string) GatewayControlResponse {
	return GatewayControlResponse{
		Type: GatewayControlTypeResponse,
		Response: GatewayControlResponseBody{
			Subtype:   "error",
			RequestID: strings.TrimSpace(requestID),
			Error:     strings.TrimSpace(message),
		},
	}
}
