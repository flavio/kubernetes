package kubewarden

import (
	"encoding/json"
)

type ValidationResponse struct {
	Accepted      bool   `json:"accepted"`
	Message       string `json:"message,omitempty"`
	Code          int    `json:"code,omitempty"`
	MutatedObject string `json:"mutated_object,omitempty"`
}

func NewValidationResponse(data []byte) (ValidationResponse, error) {
	vr := ValidationResponse{}
	err := json.Unmarshal(data, &vr)
	return vr, err
}
