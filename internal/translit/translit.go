package translit

import "strings"

// bulgarianToLatin maps Bulgarian Cyrillic characters to Latin equivalents
var bulgarianToLatin = map[rune]string{
	'а': "a", 'б': "b", 'в': "v", 'г': "g", 'д': "d", 'е': "e", 'ж': "zh",
	'з': "z", 'и': "i", 'й': "y", 'к': "k", 'л': "l", 'м': "m", 'н': "n",
	'о': "o", 'п': "p", 'р': "r", 'с': "s", 'т': "t", 'у': "u", 'ф': "f",
	'х': "h", 'ц': "ts", 'ч': "ch", 'ш': "sh", 'щ': "sht", 'ъ': "a",
	'ь': "y", 'ю': "yu", 'я': "ya",
	'А': "A", 'Б': "B", 'В': "V", 'Г': "G", 'Д': "D", 'Е': "E", 'Ж': "Zh",
	'З': "Z", 'И': "I", 'Й': "Y", 'К': "K", 'Л': "L", 'М': "M", 'Н': "N",
	'О': "O", 'П': "P", 'Р': "R", 'С': "S", 'Т': "T", 'У': "U", 'Ф': "F",
	'Х': "H", 'Ц': "Ts", 'Ч': "Ch", 'Ш': "Sh", 'Щ': "Sht", 'Ъ': "A",
	'Ь': "Y", 'Ю': "Yu", 'Я': "Ya",
}

// ToSlug converts a Bulgarian string to a URL-friendly slug
func ToSlug(s string) string {
	var result strings.Builder
	for _, r := range s {
		if repl, ok := bulgarianToLatin[r]; ok {
			result.WriteString(strings.ToLower(repl))
		} else if r == ' ' || r == '-' || r == '_' {
			result.WriteByte('-')
		} else if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' {
			result.WriteRune(r)
		}
	}
	slug := result.String()
	// Collapse multiple dashes
	for strings.Contains(slug, "--") {
		slug = strings.ReplaceAll(slug, "--", "-")
	}
	slug = strings.Trim(slug, "-")
	return strings.ToLower(slug)
}

// NormalizeCity takes a city name (possibly transliterated) and returns the Bulgarian name
func NormalizeCity(city string) string {
	// Common transliterated city names back to Bulgarian
	transliterated := map[string]string{
		"sofia":    "София",
		"sofiya":   "София",
		"varna":    "Варна",
		"burgas":   "Бургас",
		"plovdiv":  "Пловдив",
		"veliko tarnovo": "Велико Търново",
		"stara zagora":   "Стара Загора",
		"ruse":     "Русе",
		"pleven":   "Плевен",
		"shumen":   "Шумен",
		"blagoevgrad": "Благоевград",
		"pernik":   "Перник",
		"vratsa":   "Враца",
		"gabrovo":  "Габрово",
		"pazardzhik": "Пазарджик",
		"kardzhali": "Кърджали",
		"haskovo":  "Хасково",
		"sliven":   "Сливен",
		"dobrich":  "Добрич",
		"lovech":   "Ловеч",
		"montana":  "Монтана",
		"vidin":    "Видин",
		"razgrad":  "Разград",
		"targovishte": "Търговище",
		"silistra": "Силистра",
		"kyustendil": "Кюстендил",
		"yambol":   "Ямбол",
		"smolyan":  "Смолян",
	}
	if bg, ok := transliterated[strings.ToLower(city)]; ok {
		return bg
	}
	return city
}
