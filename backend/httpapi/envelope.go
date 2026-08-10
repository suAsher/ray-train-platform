package httpapi

type Error struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type Envelope[T any] struct {
	Success   bool   `json:"success"`
	Data      T      `json:"data,omitempty"`
	Error     *Error `json:"error,omitempty"`
	RequestID string `json:"request_id"`
}

func Success[T any](requestID string, data T) Envelope[T] {
	return Envelope[T]{Success: true, Data: data, RequestID: requestID}
}

func Failure[T any](requestID, code, message string) Envelope[T] {
	return Envelope[T]{
		Success:   false,
		Error:     &Error{Code: code, Message: message},
		RequestID: requestID,
	}
}
