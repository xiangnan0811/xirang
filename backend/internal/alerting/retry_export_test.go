package alerting

import "xirang/backend/internal/model"

// SetSendFn 替换生产发送函数，仅供测试使用。
// 放在 _test.go 文件中确保非测试代码无法访问。
func (w *RetryWorker) SetSendFn(fn func(model.Integration, model.Alert) error) {
	w.sendFn = fn
}
