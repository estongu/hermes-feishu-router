package model

// ImageAttachment 是已从飞书下载、等待转交给目标 Studio 的图片。
// Data 只在单次请求内存中流转，不能写入日志或数据库。
type ImageAttachment struct {
	Name      string
	MediaType string
	Data      []byte
}
