package favro

import "encoding/json"

// The Favro API's objects, as far as Dash reads them.

type organization struct {
	OrganizationID string `json:"organizationId"`
	Name           string `json:"name"`
}

type collection struct {
	CollectionID string `json:"collectionId"`
	Name         string `json:"name"`
	Archived     bool   `json:"archived"`
}

type widget struct {
	WidgetCommonID string   `json:"widgetCommonId"`
	Name           string   `json:"name"`
	Type           string   `json:"type"` // "board" or "backlog"
	CollectionIDs  []string `json:"collectionIds"`
	Archived       bool     `json:"archived"`
}

type column struct {
	ColumnID       string  `json:"columnId"`
	WidgetCommonID string  `json:"widgetCommonId"`
	Name           string  `json:"name"`
	Position       float64 `json:"position"`
}

type tag struct {
	TagID string `json:"tagId"`
	Name  string `json:"name"`
}

type customField struct {
	CustomFieldID string `json:"customFieldId"`
	Name          string `json:"name"`
	Type          string `json:"type"` // "Number", "Text", "Single select", …
	Enabled       *bool  `json:"enabled,omitempty"`
}

type user struct {
	UserID string `json:"userId"`
	Name   string `json:"name"`
	Email  string `json:"email"`
}

type card struct {
	CardID              string       `json:"cardId"`
	CardCommonID        string       `json:"cardCommonId"`
	WidgetCommonID      string       `json:"widgetCommonId"`
	ColumnID            string       `json:"columnId"`
	Name                string       `json:"name"`
	DetailedDescription string       `json:"detailedDescription"`
	Tags                []string     `json:"tags"` // tag ids
	SequentialID        int          `json:"sequentialId"`
	Archived            bool         `json:"archived"`
	NumComments         int          `json:"numComments"`
	CustomFields        []cardField  `json:"customFields"`
	Dependencies        []dependency `json:"dependencies"`
}

// cardField is a custom field's value on a card; a Number field's is its total.
type cardField struct {
	CustomFieldID string          `json:"customFieldId"`
	Total         *float64        `json:"total,omitempty"`
	Value         json.RawMessage `json:"value,omitempty"`
}

// dependency links a card to another: isBefore means this card comes
// first (the other one depends on it); otherwise this one depends on it.
type dependency struct {
	CardID   string `json:"cardId"`
	IsBefore bool   `json:"isBefore"`
}

type comment struct {
	CommentID    string `json:"commentId"`
	CardCommonID string `json:"cardCommonId"`
	UserID       string `json:"userId"`
	Comment      string `json:"comment"`
	Created      string `json:"created"`
	LastUpdated  string `json:"lastUpdated"`
}

type webhook struct {
	WebhookID      string `json:"webhookId"`
	WidgetCommonID string `json:"widgetCommonId"`
	Name           string `json:"name"`
	PostToURL      string `json:"postToUrl"`
}

// webhookCall is what Favro posts to a webhook.
type webhookCall struct {
	PayloadID string `json:"payloadId"`
	Action    string `json:"action"`
	Hook      *struct {
		URL string `json:"url"`
	} `json:"hook,omitempty"`
	Card *struct {
		CardID       string `json:"cardId"`
		CardCommonID string `json:"cardCommonId"`
	} `json:"card,omitempty"`
	Comment *struct {
		CardCommonID string `json:"cardCommonId"`
	} `json:"comment,omitempty"`
}

// webhookNotifications are the events Dash's webhooks ask for.
var webhookNotifications = []string{
	"card.created", "card.committed", "card.moved", "card.updated", "card.deleted",
	"comment.created", "comment.updated", "comment.deleted",
}
