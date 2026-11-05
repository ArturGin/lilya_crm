package analysis

import "strings"

// PoseDef is a known pose. Slugs are canonical: every analysis step (keyword
// matching, LLMs, future pose-estimation models) should map to these so the
// same pose becomes the same tag regardless of which tool found it.
//
// Match patterns are used by the keyword step against transcripts:
// space-separated tokens, a trailing "*" means "word starts with" (handles
// Russian inflection: "голуб*" matches голубь/голубя/голубе), otherwise
// the word must match exactly. Text is lower-cased and ё→е before matching.
// The longest matching pattern wins ("боковая планка" → side-plank, not plank).
type PoseDef struct {
	Slug      string
	RU        string
	EN        string
	Sanskrit  string
	BodyAreas []string
	Focus     []string
	Match     []string
}

func (p PoseDef) DisplayName(lang string) string {
	if lang == "en" {
		return p.EN
	}
	return p.RU
}

var Poses = []PoseDef{
	{"childs-pose", "Поза ребёнка", "Child's pose", "Balasana", []string{"lower-back", "hips"}, []string{"relaxation"},
		[]string{"поз* ребенк*", "баласан*", "child* pose", "balasana"}},
	{"downward-facing-dog", "Собака мордой вниз", "Downward-facing dog", "Adho Mukha Svanasana", []string{"hamstrings", "shoulders", "spine"}, []string{"flexibility"},
		[]string{"собак* морд* вниз", "собак* вниз", "адхо мукх*", "down* dog", "down* facing dog", "adho mukha"}},
	{"upward-facing-dog", "Собака мордой вверх", "Upward-facing dog", "Urdhva Mukha Svanasana", []string{"chest", "thoracic-spine", "arms-wrists"}, []string{"backbends", "chest-opening"},
		[]string{"собак* морд* вверх", "собак* вверх", "урдхв* мукх*", "up* dog", "upward facing dog"}},
	{"cobra", "Кобра", "Cobra", "Bhujangasana", []string{"lower-back", "chest"}, []string{"backbends", "chest-opening"},
		[]string{"кобр*", "бхуджанг*", "cobra", "bhujangasana"}},
	{"sphinx", "Сфинкс", "Sphinx", "Salamba Bhujangasana", []string{"lower-back"}, []string{"backbends"},
		[]string{"сфинкс*", "sphinx"}},
	{"pigeon", "Голубь", "Pigeon", "Eka Pada Rajakapotasana", []string{"hips", "glutes", "hip-flexors"}, []string{"hip-opening"},
		[]string{"голуб*", "капотасан*", "раджакапот*", "pigeon"}},
	{"figure-four", "Четвёрка лёжа", "Figure four (reclined pigeon)", "Supta Kapotasana", []string{"hips", "glutes"}, []string{"hip-opening"},
		[]string{"четверк*", "супт* капот*", "figure four", "reclined pigeon"}},
	{"cat-cow", "Кошка-корова", "Cat-cow", "Marjaryasana-Bitilasana", []string{"spine"}, []string{"mobility"},
		[]string{"кошк*", "марджари*", "битил*", "cat cow", "cat pose"}},
	{"mountain", "Поза горы", "Mountain", "Tadasana", nil, []string{"posture"},
		[]string{"тадасан*", "поз* гор*", "mountain pose", "tadasana"}},
	{"standing-forward-fold", "Наклон стоя", "Standing forward fold", "Uttanasana", []string{"hamstrings", "lower-back"}, []string{"forward-folds"},
		[]string{"уттанасан*", "наклон* стоя", "standing forward fold", "uttanasana"}},
	{"seated-forward-fold", "Наклон сидя", "Seated forward fold", "Paschimottanasana", []string{"hamstrings", "lower-back"}, []string{"forward-folds"},
		[]string{"пашчимоттан*", "пашимоттан*", "наклон* сидя", "seated forward fold", "paschimottanasana"}},
	{"wide-legged-forward-fold", "Наклон в широкой стойке", "Wide-legged forward fold", "Prasarita Padottanasana", []string{"hamstrings", "adductors"}, []string{"forward-folds"},
		[]string{"прасарит*", "наклон* в широк* стойк*", "wide legged forward fold", "prasarita"}},
	{"pyramid", "Пирамида", "Pyramid", "Parsvottanasana", []string{"hamstrings"}, []string{"forward-folds"},
		[]string{"пирамид*", "парсвоттанасан*", "pyramid pose"}},
	{"warrior-1", "Воин I", "Warrior I", "Virabhadrasana I", []string{"hip-flexors", "quadriceps", "shoulders"}, []string{"strength"},
		[]string{"воин* один", "воин* 1", "перв* воин*", "вирабхадрасан* один", "warrior one", "warrior 1"}},
	{"warrior-2", "Воин II", "Warrior II", "Virabhadrasana II", []string{"hips", "quadriceps", "adductors"}, []string{"strength", "hip-opening"},
		[]string{"воин* два", "воин* 2", "втор* воин*", "вирабхадрасан* два", "warrior two", "warrior 2"}},
	{"warrior-3", "Воин III", "Warrior III", "Virabhadrasana III", []string{"hamstrings", "glutes", "core"}, []string{"balance", "strength"},
		[]string{"воин* три", "воин* 3", "трет* воин*", "вирабхадрасан* три", "warrior three", "warrior 3"}},
	{"triangle", "Треугольник", "Triangle", "Utthita Trikonasana", []string{"hamstrings", "adductors"}, []string{"side-bends", "flexibility"},
		[]string{"треугольн*", "триконасан*", "triangle", "trikonasana"}},
	{"extended-side-angle", "Боковой угол", "Extended side angle", "Utthita Parsvakonasana", []string{"adductors", "quadriceps"}, []string{"side-bends", "strength"},
		[]string{"боков* угл*", "боков* угол", "парсваконасан*", "side angle"}},
	{"half-moon", "Полумесяц", "Half moon", "Ardha Chandrasana", []string{"hips", "ankles-feet"}, []string{"balance"},
		[]string{"полумесяц*", "ардх* чандр*", "half moon"}},
	{"tree", "Дерево", "Tree", "Vrikshasana", []string{"ankles-feet", "hips"}, []string{"balance"},
		[]string{"дерев*", "врикшасан*", "tree pose", "vrksasana", "vrikshasana"}},
	{"eagle", "Орёл", "Eagle", "Garudasana", []string{"shoulders", "hips"}, []string{"balance"},
		[]string{"орел", "орла", "орле", "гарудасан*", "eagle pose", "garudasana"}},
	{"dancer", "Танцор", "Dancer", "Natarajasana", []string{"quadriceps", "chest"}, []string{"balance", "backbends"},
		[]string{"танцор*", "натарадж*", "dancer pose", "natarajasana"}},
	{"chair", "Стул", "Chair", "Utkatasana", []string{"quadriceps", "glutes"}, []string{"strength"},
		[]string{"поз* стул*", "уткатасан*", "chair pose", "utkatasana"}},
	{"garland", "Маласана", "Garland (yogi squat)", "Malasana", []string{"hips", "ankles-feet", "adductors"}, []string{"hip-opening"},
		[]string{"маласан*", "гирлянд*", "garland pose", "malasana", "yogi squat"}},
	{"low-lunge", "Низкий выпад", "Low lunge", "Anjaneyasana", []string{"hip-flexors", "quadriceps"}, []string{"hip-opening", "flexibility"},
		[]string{"выпад*", "анджанеясан*", "low lunge", "anjaneyasana"}},
	{"lizard", "Ящерица", "Lizard", "Utthan Pristhasana", []string{"hip-flexors", "hips"}, []string{"hip-opening"},
		[]string{"ящериц*", "lizard"}},
	{"frog", "Лягушка", "Frog", "Mandukasana", []string{"adductors", "hips"}, []string{"hip-opening"},
		[]string{"лягушк*", "мандукасан*", "frog pose"}},
	{"butterfly", "Бабочка", "Butterfly (bound angle)", "Baddha Konasana", []string{"adductors", "hips"}, []string{"hip-opening"},
		[]string{"бабочк*", "баддх* конасан*", "butterfly", "bound angle"}},
	{"reclined-bound-angle", "Бабочка лёжа", "Reclined bound angle", "Supta Baddha Konasana", []string{"adductors", "hips"}, []string{"hip-opening", "relaxation"},
		[]string{"лежач* бабочк*", "бабочк* лежа", "супт* баддх*", "reclined butterfly", "reclined bound angle"}},
	{"happy-baby", "Счастливый ребёнок", "Happy baby", "Ananda Balasana", []string{"hips", "lower-back"}, []string{"hip-opening", "relaxation"},
		[]string{"счастлив* ребенк*", "ананд* баласан*", "happy baby"}},
	{"cow-face", "Голова коровы", "Cow face", "Gomukhasana", []string{"shoulders", "hips"}, []string{"hip-opening", "chest-opening"},
		[]string{"голов* коров*", "гомукхасан*", "cow face"}},
	{"lotus", "Лотос", "Lotus", "Padmasana", []string{"hips", "knees"}, []string{"hip-opening", "meditation"},
		[]string{"лотос*", "падмасан*", "lotus"}},
	{"easy-seat", "Сукхасана", "Easy seat", "Sukhasana", []string{"hips"}, []string{"meditation"},
		[]string{"сукхасан*", "по турецк*", "easy pose", "sukhasana"}},
	{"hero", "Поза героя", "Hero", "Virasana", []string{"quadriceps", "knees", "ankles-feet"}, []string{"flexibility"},
		[]string{"поз* геро*", "вирасан*", "hero pose", "virasana"}},
	{"staff", "Посох", "Staff", "Dandasana", []string{"spine", "hamstrings"}, []string{"posture"},
		[]string{"посох*", "дандасан*", "staff pose", "dandasana"}},
	{"plank", "Планка", "Plank", "Phalakasana", []string{"core", "arms-wrists", "shoulders"}, []string{"strength"},
		[]string{"планк*", "plank"}},
	{"side-plank", "Боковая планка", "Side plank", "Vasisthasana", []string{"core", "arms-wrists"}, []string{"strength", "balance"},
		[]string{"боков* планк*", "васиштхасан*", "side plank"}},
	{"chaturanga", "Чатуранга", "Four-limbed staff", "Chaturanga Dandasana", []string{"arms-wrists", "core"}, []string{"strength"},
		[]string{"чатуранг*", "чатуранг* дандасан*", "chaturanga"}},
	{"boat", "Лодка", "Boat", "Navasana", []string{"core"}, []string{"strength"},
		[]string{"лодк*", "навасан*", "boat pose", "navasana"}},
	{"crow", "Ворона", "Crow", "Bakasana", []string{"arms-wrists", "core"}, []string{"arm-balances"},
		[]string{"ворон*", "журавл*", "бакасан*", "crow pose", "bakasana"}},
	{"bridge", "Мост", "Bridge", "Setu Bandha Sarvangasana", []string{"glutes", "lower-back", "chest"}, []string{"backbends"},
		[]string{"мост*", "сету бандх*", "bridge pose", "bridge"}},
	{"wheel", "Колесо", "Wheel", "Urdhva Dhanurasana", []string{"chest", "shoulders", "spine"}, []string{"backbends"},
		[]string{"колес*", "полн* мост*", "урдхв* дханур*", "wheel pose"}},
	{"bow", "Лук", "Bow", "Dhanurasana", []string{"chest", "quadriceps", "lower-back"}, []string{"backbends"},
		[]string{"поз* лук*", "дханурасан*", "bow pose", "dhanurasana"}},
	{"locust", "Саранча", "Locust", "Salabhasana", []string{"lower-back", "glutes"}, []string{"backbends", "strength"},
		[]string{"саранч*", "шалабхасан*", "салабхасан*", "locust"}},
	{"camel", "Верблюд", "Camel", "Ustrasana", []string{"chest", "hip-flexors", "thoracic-spine"}, []string{"backbends", "chest-opening"},
		[]string{"верблюд*", "уштрасан*", "camel"}},
	{"fish", "Рыба", "Fish", "Matsyasana", []string{"chest", "neck", "thoracic-spine"}, []string{"chest-opening", "backbends"},
		[]string{"поз* рыб*", "матсиасан*", "матсьясан*", "fish pose", "matsyasana"}},
	{"puppy", "Щенок", "Extended puppy", "Uttana Shishosana", []string{"shoulders", "thoracic-spine"}, []string{"chest-opening"},
		[]string{"щенок", "щенк*", "puppy"}},
	{"thread-the-needle", "Нить в иголке", "Thread the needle", "Parsva Balasana", []string{"shoulders", "thoracic-spine"}, []string{"twists"},
		[]string{"нит* в иголк*", "иголк*", "thread the needle"}},
	{"supine-twist", "Скрутка лёжа", "Supine twist", "Supta Matsyendrasana", []string{"spine", "lower-back"}, []string{"twists"},
		[]string{"скрутк* леж*", "леж* скрутк*", "супт* матс*", "supine twist"}},
	{"seated-twist", "Скрутка сидя", "Seated twist", "Ardha Matsyendrasana", []string{"spine", "thoracic-spine"}, []string{"twists"},
		[]string{"скрутк* сидя", "матсиендр*", "матсьендр*", "ардх* матс*", "seated twist"}},
	{"gate", "Ворота", "Gate", "Parighasana", nil, []string{"side-bends"},
		[]string{"поз* ворот*", "паригхасан*", "gate pose"}},
	{"half-split", "Полушпагат", "Half split", "Ardha Hanumanasana", []string{"hamstrings"}, []string{"flexibility"},
		[]string{"полушпагат*", "ардх* хануман*", "half split"}},
	{"split", "Шпагат", "Splits", "Hanumanasana", []string{"hamstrings", "hip-flexors"}, []string{"flexibility"},
		[]string{"шпагат*", "хануманасан*", "splits"}},
	{"shoulder-stand", "Стойка на плечах", "Shoulder stand", "Sarvangasana", []string{"neck", "shoulders"}, []string{"inversions"},
		[]string{"стойк* на плеч*", "березк*", "сарвангасан*", "shoulder stand", "sarvangasana"}},
	{"plow", "Плуг", "Plow", "Halasana", []string{"spine", "neck", "hamstrings"}, []string{"inversions", "forward-folds"},
		[]string{"плуг*", "халасан*", "plow", "plough"}},
	{"headstand", "Стойка на голове", "Headstand", "Sirsasana", []string{"core", "shoulders", "neck"}, []string{"inversions"},
		[]string{"стойк* на голов*", "ширшасан*", "headstand", "sirsasana"}},
	{"legs-up-the-wall", "Ноги на стене", "Legs up the wall", "Viparita Karani", []string{"hamstrings", "lower-back"}, []string{"relaxation", "inversions"},
		[]string{"ног* на стен*", "випарит* карани", "legs up the wall", "viparita karani"}},
	{"corpse", "Шавасана", "Corpse pose", "Savasana", nil, []string{"relaxation"},
		[]string{"шавасан*", "швасан*", "шавасон*", "савасан*", "savasana", "shavasana", "corpse pose"}},
}

var poseIndex = func() map[string]PoseDef {
	m := map[string]PoseDef{}
	for _, p := range Poses {
		m[p.Slug] = p
	}
	// Common alternative slugs an LLM may produce.
	alias := map[string]string{
		"downward-dog": "downward-facing-dog", "down-dog": "downward-facing-dog", "adho-mukha-svanasana": "downward-facing-dog",
		"upward-dog": "upward-facing-dog", "up-dog": "upward-facing-dog",
		"child-s-pose": "childs-pose", "child-pose": "childs-pose", "balasana": "childs-pose",
		"savasana": "corpse", "shavasana": "corpse", "corpse-pose": "corpse",
		"pigeon-pose": "pigeon", "eka-pada-rajakapotasana": "pigeon", "reclined-pigeon": "figure-four",
		"cat-cow-pose": "cat-cow", "cat": "cat-cow", "cow": "cat-cow",
		"uttanasana": "standing-forward-fold", "forward-fold": "standing-forward-fold",
		"paschimottanasana": "seated-forward-fold", "bound-angle": "butterfly", "baddha-konasana": "butterfly",
		"warrior-i": "warrior-1", "warrior-ii": "warrior-2", "warrior-iii": "warrior-3",
		"bridge-pose": "bridge", "cobra-pose": "cobra", "tree-pose": "tree", "boat-pose": "boat", "crow-pose": "crow",
		"low-lunge-pose": "low-lunge", "anjaneyasana": "low-lunge", "malasana": "garland", "yogi-squat": "garland",
		"legs-up-wall": "legs-up-the-wall", "viparita-karani": "legs-up-the-wall",
		"seated-spinal-twist": "seated-twist", "supine-spinal-twist": "supine-twist", "reclined-twist": "supine-twist",
	}
	for a, canon := range alias {
		m[a] = m[canon]
	}
	return m
}()

// PoseBySlug finds a dictionary pose by canonical slug or known alias.
func PoseBySlug(slug string) (PoseDef, bool) {
	p, ok := poseIndex[slug]
	return p, ok
}

// poseSlugList renders "slug — English (Sanskrit)" lines for LLM prompts.
func poseSlugList() string {
	var b strings.Builder
	for _, p := range Poses {
		b.WriteString(p.Slug + " — " + p.EN)
		if p.Sanskrit != "" {
			b.WriteString(" (" + p.Sanskrit + ")")
		}
		b.WriteByte('\n')
	}
	return b.String()
}

// WhisperPrompt is a vocabulary hint for speech recognition so that Sanskrit
// pose names are spelled consistently (Whisper reads at most ~224 tokens).
func WhisperPrompt(lang string) string {
	if lang == "en" {
		return "Yoga class. Poses: savasana, balasana, adho mukha svanasana, bhujangasana, kapotasana, trikonasana, " +
			"virabhadrasana, utkatasana, malasana, baddha konasana, paschimottanasana, sarvangasana, halasana, pranayama, ujjayi."
	}
	return "Практика йоги. Асаны: шавасана, баласана, адхо мукха шванасана, бхуджангасана, капотасана, триконасана, " +
		"вирабхадрасана, уткатасана, маласана, баддха конасана, пашчимоттанасана, сарвангасана, халасана. " +
		"Раскрытие ТБС, прогибы, скрутки, пранаяма, уджайи."
}
