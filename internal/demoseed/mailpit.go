package demoseed

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

func getJSON(ctx context.Context, client *http.Client, target string, out any) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return err
	}
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("mailpit: status %d", response.StatusCode)
	}
	return json.NewDecoder(io.LimitReader(response.Body, 4<<20)).Decode(out)
}
