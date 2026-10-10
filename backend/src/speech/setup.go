package speech

import "github.com/evelyn-kk/blink-shop/backend/src/configcenter"

// FromConfig 按配置创建识别和合成供应商；off 或凭据不全时对应的返回 nil（接口返回 *_not_enabled）。
func FromConfig(c configcenter.SpeechConfig) (Recognizer, Synthesizer) {
	var rec Recognizer
	switch c.STTProvider {
	case "mock":
		rec = MockRecognizer{}
	case "xunfei":
		if r := NewXunfeiRecognizer(XunfeiRecognizer{Cred: XunfeiCredentials{AppID: c.STTAppID, APIKey: c.STTAPIKey, APISecret: c.STTAPISecret},
			BaseURL: c.STTEndpoint, Lang: c.STTLang}); r != nil {
			rec = r
		}
	}
	var syn Synthesizer
	switch c.TTSProvider {
	case "mock":
		syn = MockSynthesizer{}
	case "xunfei":
		if s := NewXunfeiSynthesizer(XunfeiSynthesizer{Cred: XunfeiCredentials{AppID: c.TTSAppID, APIKey: c.TTSAPIKey, APISecret: c.TTSAPISecret},
			BaseURL: c.TTSEndpoint, DefaultVoice: c.TTSDefaultVoice}); s != nil {
			syn = s
		}
	case "doubao":
		if s := NewDoubaoSynthesizer(DoubaoSynthesizer{AppID: c.TTSAppID, Token: c.TTSAPIKey, Cluster: c.TTSCluster, BaseURL: c.TTSEndpoint,
			DefaultVoice: c.TTSDefaultVoice}); s != nil {
			syn = s
		}
	}
	return rec, syn
}
