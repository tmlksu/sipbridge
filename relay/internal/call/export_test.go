package call

// EmitForTest は emit をテストから呼ぶためのフックである。
func (m *Manager) EmitForTest(ev Event) { m.emit(ev) }

// EventQueueSize は公開イベントのバッファ長である (テスト用)。
const EventQueueSize = eventQueueSize
