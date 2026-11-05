package analysis

// Controlled vocabularies. Body areas and focus directions are fixed lists so
// that filters stay consistent across videos; poses are open-ended (the model
// proposes a canonical English slug so the same pose merges across videos).
//
// To add a term: append here. Existing slugs must never change — they are
// stored in the database and used in URLs.

type Term struct {
	Slug string `json:"slug"`
	RU   string `json:"ru"`
	EN   string `json:"en"`
}

func (t Term) Name(lang string) string {
	if lang == "en" {
		return t.EN
	}
	return t.RU
}

var BodyAreas = []Term{
	{"hips", "Тазобедренные суставы (ТБС)", "Hips"},
	{"hip-flexors", "Сгибатели бедра", "Hip flexors"},
	{"glutes", "Ягодицы", "Glutes"},
	{"hamstrings", "Задняя поверхность бедра", "Hamstrings"},
	{"quadriceps", "Передняя поверхность бедра", "Quadriceps"},
	{"adductors", "Внутренняя поверхность бедра", "Inner thighs (adductors)"},
	{"knees", "Колени", "Knees"},
	{"ankles-feet", "Стопы и голеностопы", "Ankles & feet"},
	{"pelvic-floor", "Тазовое дно", "Pelvic floor"},
	{"lower-back", "Поясница", "Lower back"},
	{"thoracic-spine", "Грудной отдел", "Thoracic spine / upper back"},
	{"spine", "Позвоночник", "Spine"},
	{"neck", "Шея", "Neck"},
	{"shoulders", "Плечи", "Shoulders"},
	{"chest", "Грудная клетка", "Chest"},
	{"arms-wrists", "Руки и запястья", "Arms & wrists"},
	{"core", "Мышцы кора", "Core"},
}

var Focuses = []Term{
	{"hip-opening", "Раскрытие ТБС", "Hip opening"},
	{"chest-opening", "Раскрытие грудного отдела", "Chest opening"},
	{"backbends", "Прогибы", "Backbends"},
	{"forward-folds", "Наклоны", "Forward folds"},
	{"twists", "Скрутки", "Twists"},
	{"side-bends", "Боковые вытяжения", "Side bends"},
	{"inversions", "Перевёрнутые асаны", "Inversions"},
	{"arm-balances", "Баланс на руках", "Arm balances"},
	{"balance", "Баланс", "Balance"},
	{"strength", "Сила", "Strength"},
	{"flexibility", "Гибкость и растяжка", "Flexibility & stretching"},
	{"mobility", "Суставная мобильность", "Joint mobility"},
	{"posture", "Осанка", "Posture"},
	{"relaxation", "Расслабление и восстановление", "Relaxation & recovery"},
	{"breathwork", "Дыхательные практики", "Breathwork"},
	{"meditation", "Медитация", "Meditation"},
}

var Levels = []Term{
	{"beginner", "Начинающий", "Beginner"},
	{"intermediate", "Средний", "Intermediate"},
	{"advanced", "Продвинутый", "Advanced"},
	{"all-levels", "Любой уровень", "All levels"},
}

func slugs(terms []Term) []string {
	out := make([]string, len(terms))
	for i, t := range terms {
		out[i] = t.Slug
	}
	return out
}

func lookup(terms []Term, slug string) (Term, bool) {
	for _, t := range terms {
		if t.Slug == slug {
			return t, true
		}
	}
	return Term{}, false
}

// Vocabulary is exposed to clients (labels for filters, admin tag editor).
type Vocabulary struct {
	BodyAreas []Term `json:"body_area"`
	Focuses   []Term `json:"focus"`
	Levels    []Term `json:"level"`
}

func GetVocabulary() Vocabulary {
	return Vocabulary{BodyAreas: BodyAreas, Focuses: Focuses, Levels: Levels}
}
