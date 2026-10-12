package main

import (
	"context"
	"testing"

	feishuadapter "github.com/estongu/hermes-feishu-router/internal/feishu"
)

type fakeImageDownloader struct {
	data []byte
	key  string
}

func (d *fakeImageDownloader) DownloadMessageImage(_ context.Context, messageID, key string) ([]byte, error) {
	d.key = key
	if messageID != "om_message" {
		return nil, &unexpectedMediaType{got: messageID}
	}
	return d.data, nil
}

type unexpectedMediaType struct{ got string }

func (e *unexpectedMediaType) Error() string { return "unexpected media type: " + e.got }

// TestDownloadPostImages 验证图片按 image_key 下载，并根据字节内容识别 PNG 类型。
func TestDownloadPostImages(t *testing.T) {
	downloader := &fakeImageDownloader{data: []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a}}
	images, err := downloadPostImages(context.Background(), downloader, "om_message", []feishuadapter.RichTextImage{{ImageKey: "img_v3_test"}})
	if err != nil {
		t.Fatalf("downloadPostImages() error = %v", err)
	}
	if downloader.key != "img_v3_test" || len(images) != 1 {
		t.Fatalf("download result = %#v, key = %q", images, downloader.key)
	}
	if images[0].MediaType != "image/png" || images[0].Name != "feishu-image-1.png" {
		t.Fatalf("attachment = %#v", images[0])
	}
}
