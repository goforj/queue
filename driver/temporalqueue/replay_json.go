package temporalqueue

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
)

// maxReplayHistoryEvents bounds event objects before the SDK materializes a JSON capture.
const maxReplayHistoryEvents = 100000

// preflightReplayJSON counts event values without retaining their decoded object graphs.
func preflightReplayJSON(data []byte, eventLimit int) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return errors.New("Temporal replay capture is invalid")
	}
	count := 0
	for decoder.More() {
		token, err := decoder.Token()
		key, ok := token.(string)
		if err != nil || !ok {
			return errors.New("Temporal replay capture is invalid")
		}
		if key != "events" {
			if err := skipReplayJSONValue(decoder); err != nil {
				return errors.New("Temporal replay capture is invalid")
			}
			continue
		}
		token, err = decoder.Token()
		if err != nil || token != json.Delim('[') {
			return errors.New("Temporal replay capture is invalid")
		}
		for decoder.More() {
			if count >= eventLimit {
				return errors.New("Temporal replay capture exceeds event limit")
			}
			count++
			if err := skipReplayJSONValue(decoder); err != nil {
				return errors.New("Temporal replay capture is invalid")
			}
		}
		if token, err := decoder.Token(); err != nil || token != json.Delim(']') {
			return errors.New("Temporal replay capture is invalid")
		}
	}
	if token, err := decoder.Token(); err != nil || token != json.Delim('}') {
		return errors.New("Temporal replay capture is invalid")
	}
	if _, err := decoder.Token(); err != io.EOF {
		return errors.New("Temporal replay capture is invalid")
	}
	return nil
}

// skipReplayJSONValue consumes nested data iteratively so preflight does not allocate a history-sized container.
func skipReplayJSONValue(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	depth := 0
	if token == json.Delim('{') || token == json.Delim('[') {
		depth = 1
	}
	for depth > 0 {
		token, err = decoder.Token()
		if err != nil {
			return err
		}
		switch token {
		case json.Delim('{'), json.Delim('['):
			depth++
		case json.Delim('}'), json.Delim(']'):
			depth--
		}
	}
	return nil
}
