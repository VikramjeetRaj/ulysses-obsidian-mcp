package vault

// Note is a Markdown note. Listings leave Content empty.
type Note struct {
	Path    string `json:"path"` // relative to the Knowledge directory
	Content string `json:"content"`
	HashVal string `json:"hashVal"`
}

// Page describes one page of a paginated listing.
type Page struct {
	Total   int `json:"total"`
	Current int `json:"current"`
	Size    int `json:"size"`
}

// NotePage is one page of notes from ListNotes.
type NotePage struct {
	Notes []Note `json:"notes"`
	Page  Page   `json:"page"`
}

// Attachment is a non-Markdown file. Listings carry its path and size, never its content.
type Attachment struct {
	Path string `json:"path"` // relative to the Knowledge directory
	Size int64  `json:"size"`
}

// AttachmentPage is one page of attachments from ListAttachments.
type AttachmentPage struct {
	Attachments []Attachment `json:"attachments"`
	Page        Page         `json:"page"`
}
