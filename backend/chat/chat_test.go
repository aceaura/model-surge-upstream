package chat

import (
	"encoding/base64"
	"testing"

	"github.com/aceaura/model-surge-upstream/backend/apperr"
)

// "aGVsbG8=" = "hello"，一张合法的小图占位。
func tinyImage() ImageAttachment {
	return ImageAttachment{Mime: "image/png", Data: "aGVsbG8="}
}

// TestValidateImages 锁定待发消息的校验边界：纯图可发、同空拒绝、
// 张数/mime/base64/大小各有上限，且全部归为 InvalidRequest。
func TestValidateImages(t *testing.T) {
	oversize := ImageAttachment{
		Mime: "image/png",
		Data: base64.StdEncoding.EncodeToString(make([]byte, maxImageBytes+1)),
	}
	cases := []struct {
		name    string
		content string
		images  []ImageAttachment
		wantErr bool
	}{
		{name: "纯文本", content: "你好"},
		{name: "纯图片", images: []ImageAttachment{tinyImage()}},
		{name: "图文混合", content: "看图", images: []ImageAttachment{tinyImage()}},
		{name: "同空拒绝", wantErr: true},
		{name: "张数超限", content: "x",
			images: []ImageAttachment{tinyImage(), tinyImage(), tinyImage(), tinyImage(), tinyImage()},
			wantErr: true},
		{name: "mime 白名单外", content: "x",
			images:  []ImageAttachment{{Mime: "image/svg+xml", Data: "aGVsbG8="}},
			wantErr: true},
		{name: "base64 非法", content: "x",
			images:  []ImageAttachment{{Mime: "image/png", Data: "!!!"}},
			wantErr: true},
		{name: "空图片", content: "x",
			images:  []ImageAttachment{{Mime: "image/png", Data: ""}},
			wantErr: true},
		{name: "单张超限", content: "x",
			images:  []ImageAttachment{oversize},
			wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateImages(tc.content, tc.images)
			if tc.wantErr {
				if err == nil {
					t.Fatal("期望校验失败")
				}
				if !apperr.Is(err, apperr.InvalidRequest) {
					t.Fatalf("错误码 = %v，期望 invalid_request", apperr.CodeOf(err))
				}
				return
			}
			if err != nil {
				t.Fatalf("期望通过，得到 %v", err)
			}
		})
	}
}
