package httpclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/abdullahPrasetio/relay-api/internal/domain/entity"
)

var ErrUserNotFound = errors.New("user not found")

type UserClient struct {
	client  *Client
	baseURL string
}

func NewUserClient(baseURL string, opts Options) *UserClient {
	return &UserClient{client: New(opts), baseURL: baseURL}
}

func (c *UserClient) GetUser(ctx context.Context, id string) (*entity.User, error) {
	url := fmt.Sprintf("%s/users/%s", c.baseURL, id)

	resp, body, err := c.client.Get(ctx, url)
	if err != nil {
		return nil, fmt.Errorf("user_client: get user %s: %w", id, err)
	}

	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound:
		return nil, ErrUserNotFound
	default:
		return nil, fmt.Errorf("user_client: unexpected status %d for user %s", resp.StatusCode, id)
	}

	var envelope struct {
		Data entity.User `json:"data"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil, fmt.Errorf("user_client: decode response: %w", err)
	}
	return &envelope.Data, nil
}
