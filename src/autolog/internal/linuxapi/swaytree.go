package linuxapi

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
)

// i3-ipc のプロトコル。sway も i3 と同じものを話す。
//
//	"i3-ipc" + 本文の長さ (LE uint32) + 種別 (LE uint32) + 本文
const (
	i3Magic      = "i3-ipc"
	i3HeaderSize = len(i3Magic) + 4 + 4

	// i3MsgGetTree はウィンドウの木を要求する種別。
	i3MsgGetTree uint32 = 4

	// i3MaxPayload は受け取る本文の上限。木は大きくなるが、
	// 壊れた長さで際限なく確保しないための歯止め。
	i3MaxPayload = 32 << 20
)

// encodeI3Message は i3-ipc の要求を組み立てる。
func encodeI3Message(msgType uint32, payload []byte) []byte {
	buf := make([]byte, i3HeaderSize+len(payload))
	copy(buf, i3Magic)
	binary.LittleEndian.PutUint32(buf[len(i3Magic):], uint32(len(payload)))
	binary.LittleEndian.PutUint32(buf[len(i3Magic)+4:], msgType)
	copy(buf[i3HeaderSize:], payload)
	return buf
}

// decodeI3Message は i3-ipc の応答を読む。
func decodeI3Message(r io.Reader) (uint32, []byte, error) {
	header := make([]byte, i3HeaderSize)
	if _, err := io.ReadFull(r, header); err != nil {
		return 0, nil, err
	}
	if string(header[:len(i3Magic)]) != i3Magic {
		return 0, nil, fmt.Errorf("i3-ipc の応答ではない")
	}

	length := binary.LittleEndian.Uint32(header[len(i3Magic):])
	msgType := binary.LittleEndian.Uint32(header[len(i3Magic)+4:])
	if length > i3MaxPayload {
		return 0, nil, fmt.Errorf("i3-ipc の応答が大きすぎる: %d バイト", length)
	}

	payload := make([]byte, length)
	if _, err := io.ReadFull(r, payload); err != nil {
		return 0, nil, err
	}
	return msgType, payload, nil
}

// swayNode はウィンドウの木の1節点。必要な項目だけを読む。
type swayNode struct {
	Focused bool   `json:"focused"`
	Name    string `json:"name"`
	// AppID はネイティブの Wayland クライアントで入る。
	AppID string `json:"app_id"`
	PID   uint32 `json:"pid"`
	// WindowProperties は XWayland のクライアントで入る。
	WindowProperties *swayWindowProperties `json:"window_properties"`

	Nodes         []swayNode `json:"nodes"`
	FloatingNodes []swayNode `json:"floating_nodes"`
}

type swayWindowProperties struct {
	Class    string `json:"class"`
	Instance string `json:"instance"`
	Title    string `json:"title"`
}

// parseSwayTree はウィンドウの木から前面のものを取り出す。
//
// 2つ目の戻り値が false のときはどこにも焦点が無い。
func parseSwayTree(data []byte) (ForegroundWindow, bool, error) {
	var root swayNode
	if err := json.Unmarshal(data, &root); err != nil {
		return ForegroundWindow{}, false, fmt.Errorf("ウィンドウの木を読めない: %w", err)
	}

	node, ok := findFocusedNode(&root)
	if !ok {
		return ForegroundWindow{}, false, nil
	}

	info := ForegroundWindow{
		WindowTitle: node.Name,
		PID:         node.PID,
	}
	// ネイティブの Wayland は app_id、XWayland は WM_CLASS の class。
	info.AppName = node.AppID
	if info.AppName == "" && node.WindowProperties != nil {
		info.AppName = firstNonEmptyString(node.WindowProperties.Class, node.WindowProperties.Instance)
	}
	if info.WindowTitle == "" && node.WindowProperties != nil {
		info.WindowTitle = node.WindowProperties.Title
	}

	if info.AppName == "" && info.WindowTitle == "" {
		return ForegroundWindow{}, false, nil
	}
	return info, true, nil
}

// swayNodeKeys は .desktop を引くための手がかりを取り出す。
func swayNodeKeys(info ForegroundWindow) desktopKeys {
	return desktopKeys{AppID: info.AppName, Class: info.AppName, ExecName: info.AppName}
}

// findFocusedNode は焦点のある節点を探す。
//
// 焦点は木のどこか1つだけに立つ。浮いているウィンドウも見る。
func findFocusedNode(node *swayNode) (*swayNode, bool) {
	if node.Focused && (node.AppID != "" || node.WindowProperties != nil || node.PID != 0) {
		return node, true
	}
	for i := range node.Nodes {
		if found, ok := findFocusedNode(&node.Nodes[i]); ok {
			return found, true
		}
	}
	for i := range node.FloatingNodes {
		if found, ok := findFocusedNode(&node.FloatingNodes[i]); ok {
			return found, true
		}
	}
	return nil, false
}

// firstNonEmptyString は最初の空でない文字列を返す。
func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
