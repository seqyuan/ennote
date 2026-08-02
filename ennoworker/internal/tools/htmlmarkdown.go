package tools

import (
	"bytes"
	"io"
	"strings"
	"sync"

	"github.com/JohannesKaufmann/html-to-markdown/v2/converter"
	"github.com/JohannesKaufmann/html-to-markdown/v2/plugin/base"
	"github.com/JohannesKaufmann/html-to-markdown/v2/plugin/commonmark"
	"golang.org/x/net/html"
)

var (
	htmlConverter     *converter.Converter
	htmlConverterInit sync.Once
)

// Non-content HTML elements whose subtrees are stripped before conversion.
var htmlStripTags = map[string]bool{
	"script": true, "style": true, "head": true, "nav": true,
	"footer": true, "header": true, "aside": true, "form": true,
	"iframe": true, "svg": true, "template": true, "noscript": true,
}

func getHTMLConverter() *converter.Converter {
	htmlConverterInit.Do(func() {
		htmlConverter = converter.NewConverter(
			converter.WithPlugins(base.NewBasePlugin(), commonmark.NewCommonmarkPlugin()),
		)
	})
	return htmlConverter
}

// htmlToMarkdown converts an HTML reader to plain Markdown text after
// stripping non-content subtrees. Returns the converted UTF-8 string.
// This function is safe for concurrent use.
func htmlToMarkdown(r io.Reader) (string, error) {
	doc, err := html.Parse(r)
	if err != nil {
		return "", err
	}
	stripNonContent(doc)
	var buf bytes.Buffer
	if err := html.Render(&buf, doc); err != nil {
		return "", err
	}
	markdown, err := getHTMLConverter().ConvertReader(&buf)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(markdown)), nil
}

// stripNonContent removes subtrees for non-content tags from the parsed HTML tree.
func stripNonContent(node *html.Node) {
	if node.Type == html.ElementNode && htmlStripTags[node.Data] {
		node.FirstChild = nil
		node.LastChild = nil
		return
	}
	for child := node.FirstChild; child != nil; child = child.NextSibling {
		stripNonContent(child)
	}
}
