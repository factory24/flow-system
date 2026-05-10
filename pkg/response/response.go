package response

import (
	"encoding/json"
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
