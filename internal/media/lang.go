package media

import "strings"

// Locale selects the language of display titles: "en" (as Jellyfin) or "de".
type Locale string

type words struct {
	Default, Forced, External, HearingImpaired, Original, Undefined, Chapter string
}

var localeWords = map[Locale]words{
	"en": {"Default", "Forced", "External", "Hearing Impaired", "Original", "Und", "Chapter"},
	"de": {"Standard", "Erzwungen", "Extern", "Hörgeschädigt", "Original", "Undefiniert", "Kapitel"},
}

func (l Locale) words() words {
	if w, ok := localeWords[l]; ok {
		return w
	}
	return localeWords["en"]
}

// languageNames maps ISO 639-2 (bibliographic and terminology) and ISO
// 639-1 codes to English and German names.
var languageNames = map[string][2]string{}

func init() {
	for _, l := range []struct {
		codes  string
		en, de string
	}{
		{"ger deu de", "German", "Deutsch"},
		{"eng en", "English", "Englisch"},
		{"fre fra fr", "French", "Französisch"},
		{"spa es", "Spanish", "Spanisch"},
		{"ita it", "Italian", "Italienisch"},
		{"jpn ja", "Japanese", "Japanisch"},
		{"rus ru", "Russian", "Russisch"},
		{"dut nld nl", "Dutch", "Niederländisch"},
		{"swe sv", "Swedish", "Schwedisch"},
		{"dan da", "Danish", "Dänisch"},
		{"nor nob nno no nb nn", "Norwegian", "Norwegisch"},
		{"fin fi", "Finnish", "Finnisch"},
		{"pol pl", "Polish", "Polnisch"},
		{"por pt", "Portuguese", "Portugiesisch"},
		{"tur tr", "Turkish", "Türkisch"},
		{"cze ces cs", "Czech", "Tschechisch"},
		{"slo slk sk", "Slovak", "Slowakisch"},
		{"slv sl", "Slovenian", "Slowenisch"},
		{"hun hu", "Hungarian", "Ungarisch"},
		{"gre ell el", "Greek", "Griechisch"},
		{"kor ko", "Korean", "Koreanisch"},
		{"chi zho zh", "Chinese", "Chinesisch"},
		{"ara ar", "Arabic", "Arabisch"},
		{"heb he", "Hebrew", "Hebräisch"},
		{"hin hi", "Hindi", "Hindi"},
		{"tha th", "Thai", "Thailändisch"},
		{"ice isl is", "Icelandic", "Isländisch"},
		{"ukr uk", "Ukrainian", "Ukrainisch"},
		{"rum ron ro", "Romanian", "Rumänisch"},
		{"hrv hr", "Croatian", "Kroatisch"},
		{"srp sr", "Serbian", "Serbisch"},
		{"bul bg", "Bulgarian", "Bulgarisch"},
		{"est et", "Estonian", "Estnisch"},
		{"lav lv", "Latvian", "Lettisch"},
		{"lit lt", "Lithuanian", "Litauisch"},
		{"cat ca", "Catalan", "Katalanisch"},
		{"vie vi", "Vietnamese", "Vietnamesisch"},
		{"ind id", "Indonesian", "Indonesisch"},
		{"may msa ms", "Malay", "Malaiisch"},
		{"per fas fa", "Persian", "Persisch"},
		{"tam ta", "Tamil", "Tamil"},
		{"tel te", "Telugu", "Telugu"},
		{"lat la", "Latin", "Latein"},
		{"gsw", "Swiss German", "Schweizerdeutsch"},
		{"fil tgl tl", "Filipino", "Filipino"},
	} {
		for _, c := range strings.Fields(l.codes) {
			languageNames[c] = [2]string{l.en, l.de}
		}
	}
}

// specialLanguages are codes that say nothing about the language.
var specialLanguages = map[string]bool{"mis": true, "mul": true, "und": true, "zxx": true}

// LanguageName returns the display name of a language code, or the code
// itself with its first letter capitalised.
func (l Locale) LanguageName(code string) string {
	if n, ok := languageNames[strings.ToLower(code)]; ok {
		if l == "de" {
			return n[1]
		}
		return n[0]
	}
	return firstUpper(code)
}

func firstUpper(s string) string {
	if s == "" {
		return s
	}
	r := []rune(s)
	r[0] = []rune(strings.ToUpper(string(r[0])))[0]
	return string(r)
}
