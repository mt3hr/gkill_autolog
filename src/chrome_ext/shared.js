// Service Worker と設定画面が共有する定数。
//
// 以前は両方で同じ文字列を宣言していた。片方だけ直すと、設定画面が書いた値を
// Service Worker が読まない（保存したのに送られない）という、気づきにくい
// 壊れ方をするので、1か所にまとめてある。

// chrome.storage.local のキー。
export const KEY_SETTINGS = "settings";
export const KEY_QUEUE = "queue";

// DEFAULT_ENDPOINT は Chrome の受け口 (autolog collect が開く HTTP サーバ) の既定。
// Go 側の config.DefaultIngestAddr と揃えること。
export const DEFAULT_ENDPOINT = "http://127.0.0.1:19921/ingest";
