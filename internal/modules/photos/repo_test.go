package photos

import (
	"strings"
	"testing"
)

// 存储文件名同时受这里与迁移中 CHECK 的约束：32 位小写十六进制 + 白名单扩展名。
func TestPhotoFileName(t *testing.T) {
	sum := strings.Repeat("0123456789abcdef", 4) // 64 位十六进制，模拟 content sha256 摘要
	for _, ext := range []string{".jpg", ".jpeg", ".png", ".webp"} {
		name, err := photoFileName(sum, ext)
		if err != nil {
			t.Fatalf("%s: %v", ext, err)
		}
		if name != sum[:hashPrefixLen]+ext {
			t.Fatalf("%s -> %q", ext, name)
		}
		if !validFileName(name) {
			t.Fatalf("生成的文件名不可服务: %q", name)
		}
	}
	for _, tc := range []struct{ sum, ext string }{
		{"0123", ".jpg"},                // 摘要不足 128 位
		{sum[:hashPrefixLen-1], ".png"}, // 只有 31 位十六进制
		{sum, ""},                       // 缺少扩展名
		{sum, ".gif"},                   // 不支持的图片格式
		{sum, ".JPG"},                   // 必须小写（迁移 CHECK 要求）
		{sum, ".jp"},                    // 不完整的扩展名
		{strings.ToUpper(sum), ".jpg"},  // 摘要必须小写
		{sum, ".jpg/../secret.jpg"},     // 注入路径分隔符
		{strings.Repeat("g", hashPrefixLen), ".jpg"}, // 摘要含非十六进制字符
	} {
		if _, err := photoFileName(tc.sum, tc.ext); err == nil {
			t.Fatalf("接受了非法文件名 %q%q", tc.sum, tc.ext)
		}
	}
}

func TestValidFileName(t *testing.T) {
	good := strings.Repeat("ab", 16) + ".webp"
	if !validFileName(good) {
		t.Fatalf("拒绝了合法文件名 %q", good)
	}
	for _, bad := range []string{
		"",
		".",
		"..",
		"../../etc/passwd",
		"/etc/passwd",
		"sub/" + good,
		strings.Repeat("ab", 16) + "/secret.jpg",
		strings.Repeat("ab", 32) + ".jpg",     // 完整 sha256，不是存储名
		strings.Repeat("ab", 16) + ".jpg.png", // 只允许一个扩展名
		strings.Repeat("ab", 16) + ".JPG",     // 扩展名必须小写
		strings.Repeat("a", 31) + ".jpg",
		strings.Repeat("g", 32) + ".jpg",
		".upload-123456", // 上传临时文件不可服务
	} {
		if validFileName(bad) {
			t.Fatalf("接受了非法文件名 %q", bad)
		}
	}
}
