package web

import (
	"strings"
	"testing"
)

// TestRenderMarkdownTransform covers the linkRel AST transformer and the
// renderMarkdown function that drives it.
func TestRenderMarkdownTransform(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		input     string
		wantRel   bool
		wantText  string
		wantEmpty bool
	}{
		{
			name:      "empty string returns empty HTML",
			input:     "",
			wantEmpty: true,
		},
		{
			name:      "whitespace-only returns empty HTML",
			input:     "   \n\t  ",
			wantEmpty: true,
		},
		{
			name:    "link gets nofollow noopener",
			input:   "[click here](https://example.com)",
			wantRel: true,
		},
		{
			name:     "plain text renders without rel attribute",
			input:    "just some **bold** text",
			wantText: "bold",
			wantRel:  false,
		},
		{
			name:    "multiple links all get the rel attribute",
			input:   "[a](https://a.example) and [b](https://b.example)",
			wantRel: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := string(renderMarkdown(tt.input))
			if tt.wantEmpty && got != "" {
				t.Errorf("renderMarkdown(%q) = %q; want empty string", tt.input, got)
			}
			if tt.wantRel && !strings.Contains(got, `rel="nofollow noopener"`) {
				t.Errorf("renderMarkdown(%q) = %q; want rel=\"nofollow noopener\"", tt.input, got)
			}
			if !tt.wantRel && !tt.wantEmpty && strings.Contains(got, `rel="`) {
				t.Errorf("renderMarkdown(%q) = %q; unexpected rel attribute", tt.input, got)
			}
			if tt.wantText != "" && !strings.Contains(got, tt.wantText) {
				t.Errorf("renderMarkdown(%q) = %q; want to contain %q", tt.input, got, tt.wantText)
			}
		})
	}
}

// TestRenderMarkdownSanitizesScript confirms that raw HTML tags in the
// markdown source are stripped by the sanitiser and never reach the output.
func TestRenderMarkdownSanitizesScript(t *testing.T) {
	t.Parallel()

	got := string(renderMarkdown("hello <script>alert(1)</script> world"))
	if strings.Contains(got, "<script") {
		t.Errorf("renderMarkdown output contains <script>: %q", got)
	}
	if strings.Contains(got, "</script>") {
		t.Errorf("renderMarkdown output contains </script>: %q", got)
	}
}
