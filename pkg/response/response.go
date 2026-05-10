package response

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"time"
)

type ApiResponse[T any] struct {
	Success   bool     `json:"success,omitempty"`
	Timestamp int64    `json:"timestamp,omitempty"`
	Message   string   `json:"message"`
	Errors    []string `json:"errors,omitempty"`
	Data      T        `json:"data"`
}

func NewApiResponse() *ApiResponse[any] {
	return &ApiResponse[any]{
		Success:   true,
		Timestamp: time.Now().UnixMilli(),
		Errors:    []string{},
		Data:      nil,
	}
}

func NewApiResponseWithData[T any](data T, message ...string) *ApiResponse[T] {
	msg := "success"
	if len(message) > 0 {
		msg = message[0]
	}
	return &ApiResponse[T]{
		Success:   true,
		Timestamp: time.Now().UnixMilli(),
		Message:   msg,
		Data:      data,
	}
}

func NewApiResponseWithMessage(message string) *ApiResponse[any] {
	return &ApiResponse[any]{
		Success:   true,
		Timestamp: time.Now().UnixMilli(),
		Message:   message,
	}
}

func (t ApiResponse[T]) String() string {
	jsonBytes, _ := json.Marshal(t)
	return string(jsonBytes)
}

type ErrorResponse struct {
	Success   bool     `json:"success" example:"false"`
	Timestamp int64    `json:"timestamp" example:"1719500184656"`
	Message   string   `json:"message,omitempty" `
	Errors    []string `json:"errors,omitempty"`
}

func NewErrorResponse(errors ...string) *ErrorResponse {
	return &ErrorResponse{
		Success:   false,
		Timestamp: time.Now().UnixMilli(),
		Errors:    errors,
	}
}

// Event is a generic structure for creating typed events to be sent.
type Event[T any] struct {
	Topic     string `json:"topic"`
	EventType string `json:"eventType"`
	Timestamp int64  `json:"timestamp"`
	Payload   T      `json:"payload"`
}

// EventHeader allows inspection of event metadata before unmarshalling the full payload.
type EventHeader struct {
	Topic     string          `json:"topic"`
	EventType string          `json:"eventType"`
	Timestamp int64           `json:"timestamp"`
	Payload   json.RawMessage `json:"payload"`
}

// NewEvent creates a new typed event, ready to be serialized and sent.
func NewEvent(topic, eventType string, payload any) *Event[any] {
	return &Event[any]{
		Topic:     topic,
		EventType: eventType,
		Timestamp: time.Now().UnixMilli(),
		Payload:   payload,
	}
}

// ParseEventHeader unmarshals raw bytes into an EventHeader.
func ParseEventHeader(data []byte) (*EventHeader, error) {
	var header EventHeader
	if err := json.Unmarshal(data, &header); err != nil {
		return nil, fmt.Errorf("failed to parse event header: %w", err)
	}
	return &header, nil
}

// UnmarshalPayload unmarshals the raw payload from an EventHeader into a specific target struct.
func (h *EventHeader) UnmarshalPayload(target any) error {
	if len(h.Payload) == 0 || string(h.Payload) == "null" {
		return errors.New("event has no payload to unmarshal")
	}
	if err := json.Unmarshal(h.Payload, target); err != nil {
		return fmt.Errorf("failed to unmarshal event payload for type %T: %w", target, err)
	}
	return nil
}

func (h *EventHeader) PrettyLog() {
	log.Println("============================== Consumer ===============================")
	log.Printf("Topic: %s", h.Topic)
	log.Printf("EventType: %s", h.EventType)
	log.Printf("Timestamp: %d", h.Timestamp)

	payloadStr, err := json.MarshalIndent(h.Payload, "", "  ")
	if err != nil {
		log.Printf("Failed to pretty-print payload: %v", err)
	} else {
		log.Printf("Payload:\n%s", payloadStr)
	}

	log.Println("==============================================================================")
}

func (h *EventHeader) RawLog() {
	raw, err := json.MarshalIndent(h, "", "  ")
	if err != nil {
		log.Printf("Failed to marshal EventHeader: %v", err)
		return
	}
	log.Println("===== Raw EventHeader =====")
	log.Println(string(raw))
	log.Println("===========================")
}

func (e *Event[T]) Bytes() ([]byte, error) {
	return json.Marshal(e)
}

func (e *Event[T]) String() (string, error) {
	bytes, err := e.Bytes()
	if err != nil {
		return "", err
	}
	return string(bytes), nil
}
