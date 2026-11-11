package mcp

import (
	"agentnewsfeed/internal/feed"
	"agentnewsfeed/internal/strictjson"
)

func validateArgumentObject(body []byte) error {
	if strictjson.ValidateObject(body) != nil {
		return feed.ErrInvalid
	}
	return nil
}
