// Command gendata writes data/products.json: a fictional pharmacy catalogue
// for the mock adapter. Output is deterministic (fixed seed), so re-running it
// gives the same file. Edit the templates below and re-run to change the data.
//
//	go run ./cmd/gendata            # writes data/products.json
//	go run ./cmd/gendata -out x.json
//
// All brands are invented. Active ingredients are generic names.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"math"
	"math/rand/v2"
	"os"
	"path/filepath"
	"time"

	"shopper/internal/commerce"
)

const target = 150

type brand struct{ name, short, ar string }

var (
	sahra  = brand{"Sahra Pharma", "Sahra", "صحراء"}
	nakhel = brand{"Nakheel Health", "Nakheel", "نخيل"}
	wahat  = brand{"Wahat Labs", "Wahat", "واحات"}
	marjan = brand{"Marjan Care", "Marjan", "مرجان"}
	ghaim  = brand{"Ghaim Baby", "Ghaim", "غيم"}
	yasmin = brand{"Yasmin Derma", "Yasmin", "ياسمين"}
	barq   = brand{"Barq Nutrition", "Barq", "برق"}
	rawda  = brand{"Rawda Naturals", "Rawda", "روضة"}
)

// Primary and secondary (generic) brand per category.
var categoryBrands = map[string][2]brand{
	"pain-relief":   {sahra, nakhel},
	"cold-flu":      {nakhel, wahat},
	"allergy":       {wahat, sahra},
	"digestive":     {marjan, nakhel},
	"vitamins":      {barq, rawda},
	"skin-care":     {yasmin, marjan},
	"baby-care":     {ghaim, yasmin},
	"oral-care":     {marjan, wahat},
	"first-aid":     {wahat, marjan},
	"diabetes-care": {nakhel, sahra},
	"prescription":  {sahra, wahat},
}

type unit struct{ one, many, ar string }

var units = map[string]unit{
	"tab":     {"tablet", "tablets", "قرص"},
	"cap":     {"capsule", "capsules", "كبسولة"},
	"ml":      {"ml", "ml", "مل"},
	"g":       {"g", "g", "غ"},
	"softgel": {"softgel", "softgels", "كبسولة جيلاتينية"},
	"sachet":  {"sachet", "sachets", "كيس"},
	"pc":      {"piece", "pieces", "قطعة"},
	"strip":   {"strip", "strips", "شريط"},
	"loz":     {"lozenge", "lozenges", "قرص استحلاب"},
	"gummy":   {"gummy", "gummies", "حلوى"},
	"eff":     {"effervescent tablet", "effervescent tablets", "قرص فوار"},
	"dose":    {"dose", "doses", "جرعة"},
	"wipe":    {"wipe", "wipes", "منديل"},
	"lancet":  {"lancet", "lancets", "إبرة"},
}

type size struct {
	n    int
	unit string
	mult float64 // price multiplier vs the template base price
}

func (s size) en() string {
	u := units[s.unit]
	if s.n == 1 {
		return "1 " + u.one
	}
	return fmt.Sprintf("%d %s", s.n, u.many)
}
func (s size) ar() string { return fmt.Sprintf("%d %s", s.n, units[s.unit].ar) }

type tmpl struct {
	cat, en, ar, ing string
	rx               bool
	age              int
	price            float64 // SAR for sizes[0] at mult 1
	descEN, descAR   string
	sizes            []size
}

func sz(n int, u string, m float64) size { return size{n, u, m} }

var templates = []tmpl{
	// pain-relief
	{"pain-relief", "Paracetamol 500 mg Tablets", "أقراص باراسيتامول 500 ملغ", "paracetamol", false, 12, 6.50,
		"Relieves mild to moderate pain and reduces fever.", "يخفف الآلام الخفيفة إلى المتوسطة ويخفض الحرارة.",
		[]size{sz(24, "tab", 1), sz(48, "tab", 1.8)}},
	{"pain-relief", "Children's Paracetamol Suspension 120 mg/5 ml", "معلق باراسيتامول للأطفال 120 ملغ/5 مل", "paracetamol", false, 0, 11.25,
		"Strawberry-flavoured fever and pain relief for children from 3 months.", "خافض للحرارة ومسكن للألم للأطفال من عمر 3 أشهر بنكهة الفراولة.",
		[]size{sz(100, "ml", 1), sz(150, "ml", 1.4)}},
	{"pain-relief", "Ibuprofen 400 mg Tablets", "أقراص إيبوبروفين 400 ملغ", "ibuprofen", false, 12, 9.75,
		"Anti-inflammatory pain relief for headaches, back pain and period pain.", "مسكن مضاد للالتهاب لآلام الرأس والظهر وآلام الدورة الشهرية.",
		[]size{sz(20, "tab", 1), sz(30, "tab", 1.4)}},
	{"pain-relief", "Children's Ibuprofen Suspension 100 mg/5 ml", "معلق إيبوبروفين للأطفال 100 ملغ/5 مل", "ibuprofen", false, 0, 14.50,
		"Orange-flavoured relief from fever and teething pain for children from 3 months.", "يخفف الحرارة وآلام التسنين للأطفال من عمر 3 أشهر بنكهة البرتقال.",
		[]size{sz(100, "ml", 1), sz(200, "ml", 1.7)}},
	{"pain-relief", "Diclofenac 1% Gel", "جل ديكلوفيناك 1٪", "diclofenac", false, 14, 18.00,
		"Topical relief for muscle and joint pain.", "مسكن موضعي لآلام العضلات والمفاصل.",
		[]size{sz(50, "g", 1), sz(100, "g", 1.7)}},
	{"pain-relief", "Naproxen 500 mg Tablets", "أقراص نابروكسين 500 ملغ", "naproxen", true, 16, 21.00,
		"Prescription anti-inflammatory for arthritis and acute pain.", "مضاد التهاب يصرف بوصفة لعلاج التهاب المفاصل والألم الحاد.",
		[]size{sz(20, "tab", 1)}},

	// cold-flu
	{"cold-flu", "Cold & Flu Day Capsules", "كبسولات البرد والإنفلونزا النهارية", "paracetamol, pseudoephedrine", false, 12, 16.50,
		"Relieves fever, aches and blocked nose without drowsiness.", "يخفف الحرارة والآلام واحتقان الأنف دون التسبب بالنعاس.",
		[]size{sz(16, "cap", 1), sz(24, "cap", 1.4)}},
	{"cold-flu", "Dry Cough Syrup", "شراب السعال الجاف", "dextromethorphan", false, 6, 19.25,
		"Soothes dry, tickly coughs.", "يهدئ السعال الجاف والمزعج.",
		[]size{sz(100, "ml", 1), sz(200, "ml", 1.7)}},
	{"cold-flu", "Chesty Cough Syrup", "شراب السعال المصحوب بالبلغم", "guaifenesin", false, 6, 19.25,
		"Loosens mucus to relieve chesty coughs.", "يساعد على تذويب البلغم وتخفيف السعال الصدري.",
		[]size{sz(100, "ml", 1), sz(200, "ml", 1.7)}},
	{"cold-flu", "Saline Nasal Spray", "بخاخ أنف بالمحلول الملحي", "sodium chloride", false, 0, 13.00,
		"Gently clears and moisturises the nose. Suitable for daily use.", "ينظف الأنف ويرطبه بلطف ومناسب للاستخدام اليومي.",
		[]size{sz(30, "ml", 1), sz(100, "ml", 2.2)}},
	{"cold-flu", "Honey & Lemon Throat Lozenges", "أقراص استحلاب للحلق بالعسل والليمون", "amylmetacresol, dichlorobenzyl alcohol", false, 6, 12.50,
		"Antiseptic lozenges for sore throats.", "أقراص استحلاب مطهرة لالتهاب الحلق.",
		[]size{sz(16, "loz", 1), sz(36, "loz", 2)}},
	{"cold-flu", "Xylometazoline 0.1% Nasal Spray", "بخاخ أنف زايلوميتازولين 0.1٪", "xylometazoline", false, 12, 10.75,
		"Fast relief from nasal congestion. Do not use for more than 7 days.", "يخفف احتقان الأنف بسرعة. لا يستخدم لأكثر من 7 أيام.",
		[]size{sz(10, "ml", 1)}},

	// allergy
	{"allergy", "Cetirizine 10 mg Tablets", "أقراص سيتريزين 10 ملغ", "cetirizine", false, 6, 11.50,
		"Once-daily relief from hay fever and allergy symptoms.", "قرص واحد يومياً لتخفيف أعراض حساسية الأنف والحساسية الموسمية.",
		[]size{sz(10, "tab", 1), sz(30, "tab", 2.4)}},
	{"allergy", "Loratadine 10 mg Tablets", "أقراص لوراتادين 10 ملغ", "loratadine", false, 6, 12.75,
		"Non-drowsy 24-hour allergy relief.", "يخفف أعراض الحساسية لمدة 24 ساعة دون نعاس.",
		[]size{sz(10, "tab", 1), sz(30, "tab", 2.4)}},
	{"allergy", "Fexofenadine 180 mg Tablets", "أقراص فيكسوفينادين 180 ملغ", "fexofenadine", false, 12, 24.00,
		"Non-drowsy relief from seasonal allergies and hives.", "يخفف الحساسية الموسمية والشرى دون نعاس.",
		[]size{sz(10, "tab", 1), sz(30, "tab", 2.5)}},
	{"allergy", "Allergy Eye Drops", "قطرة عين للحساسية", "sodium cromoglicate", false, 6, 17.50,
		"Relieves itchy, watery eyes caused by allergies.", "تخفف حكة العين ودموعها الناتجة عن الحساسية.",
		[]size{sz(10, "ml", 1)}},

	// digestive
	{"digestive", "Antacid Chewable Tablets", "أقراص مضغ مضادة للحموضة", "calcium carbonate", false, 12, 9.00,
		"Fast relief from heartburn and indigestion. Mint flavour.", "تخفيف سريع لحرقة المعدة وعسر الهضم بنكهة النعناع.",
		[]size{sz(24, "tab", 1), sz(48, "tab", 1.8)}},
	{"digestive", "Omeprazole 20 mg Capsules", "كبسولات أوميبرازول 20 ملغ", "omeprazole", false, 18, 22.50,
		"Reduces stomach acid for frequent heartburn.", "يقلل حمض المعدة لعلاج الحرقة المتكررة.",
		[]size{sz(14, "cap", 1), sz(28, "cap", 1.8)}},
	{"digestive", "Oral Rehydration Salts", "أملاح الإرواء الفموي", "sodium chloride, glucose, potassium chloride", false, 0, 8.25,
		"Replaces fluids and minerals lost through diarrhoea or heat.", "يعوض السوائل والأملاح المفقودة بسبب الإسهال أو الحر.",
		[]size{sz(6, "sachet", 1), sz(20, "sachet", 3)}},
	{"digestive", "Loperamide 2 mg Capsules", "كبسولات لوبيراميد 2 ملغ", "loperamide", false, 12, 10.50,
		"Fast relief from short-term diarrhoea.", "يوقف الإسهال المؤقت بسرعة.",
		[]size{sz(6, "cap", 1), sz(12, "cap", 1.7)}},
	{"digestive", "Probiotic Daily Capsules", "كبسولات بروبيوتيك يومية", "lactobacillus rhamnosus", false, 3, 54.00,
		"Supports healthy digestion and gut flora.", "يدعم الهضم الصحي وتوازن بكتيريا الأمعاء.",
		[]size{sz(30, "cap", 1), sz(60, "cap", 1.8)}},
	{"digestive", "Lactulose Oral Solution", "محلول لاكتولوز فموي", "lactulose", false, 1, 23.75,
		"Gentle relief from constipation.", "يخفف الإمساك بلطف.",
		[]size{sz(200, "ml", 1), sz(300, "ml", 1.4)}},

	// vitamins
	{"vitamins", "Vitamin D3 1000 IU Softgels", "كبسولات فيتامين د3 1000 وحدة دولية", "cholecalciferol", false, 12, 29.00,
		"Supports healthy bones, teeth and immunity.", "يدعم صحة العظام والأسنان والمناعة.",
		[]size{sz(60, "softgel", 1), sz(120, "softgel", 1.75)}},
	{"vitamins", "Vitamin C 1000 mg Effervescent", "أقراص فوارة فيتامين سي 1000 ملغ", "ascorbic acid", false, 12, 15.50,
		"Orange-flavoured immune support.", "دعم للمناعة بنكهة البرتقال.",
		[]size{sz(10, "eff", 1), sz(20, "eff", 1.8)}},
	{"vitamins", "Women's Multivitamin", "فيتامينات متعددة للنساء", "multivitamin, iron, folic acid", false, 18, 62.00,
		"Daily vitamins and minerals formulated for women.", "فيتامينات ومعادن يومية مخصصة للنساء.",
		[]size{sz(30, "tab", 1), sz(90, "tab", 2.6)}},
	{"vitamins", "Men's Multivitamin", "فيتامينات متعددة للرجال", "multivitamin, zinc", false, 18, 62.00,
		"Daily vitamins and minerals formulated for men.", "فيتامينات ومعادن يومية مخصصة للرجال.",
		[]size{sz(30, "tab", 1), sz(90, "tab", 2.6)}},
	{"vitamins", "Kids Multivitamin Gummies", "حلوى فيتامينات متعددة للأطفال", "multivitamin", false, 3, 45.00,
		"Fruit-flavoured daily vitamins for children.", "فيتامينات يومية للأطفال بنكهة الفواكه.",
		[]size{sz(30, "gummy", 1), sz(60, "gummy", 1.8)}},
	{"vitamins", "Omega-3 Fish Oil 1000 mg", "زيت السمك أوميغا 3 1000 ملغ", "EPA, DHA", false, 12, 49.50,
		"Supports heart, brain and eye health.", "يدعم صحة القلب والدماغ والعينين.",
		[]size{sz(60, "softgel", 1), sz(120, "softgel", 1.8)}},
	{"vitamins", "Iron & Folic Acid Tablets", "أقراص الحديد وحمض الفوليك", "ferrous fumarate, folic acid", false, 18, 26.00,
		"Helps reduce tiredness and supports healthy blood.", "يساعد على تقليل التعب ويدعم صحة الدم.",
		[]size{sz(30, "tab", 1), sz(60, "tab", 1.8)}},
	{"vitamins", "Zinc 25 mg Tablets", "أقراص زنك 25 ملغ", "zinc gluconate", false, 12, 24.50,
		"Supports immunity and healthy skin.", "يدعم المناعة وصحة البشرة.",
		[]size{sz(60, "tab", 1), sz(120, "tab", 1.8)}},
	{"vitamins", "Magnesium 400 mg Tablets", "أقراص مغنيسيوم 400 ملغ", "magnesium citrate", false, 18, 38.00,
		"Supports muscles, nerves and restful sleep.", "يدعم العضلات والأعصاب والنوم المريح.",
		[]size{sz(60, "tab", 1), sz(120, "tab", 1.8)}},
	{"vitamins", "Biotin 5000 mcg Tablets", "أقراص بيوتين 5000 ميكروغرام", "biotin", false, 18, 42.00,
		"Supports healthy hair, skin and nails.", "يدعم صحة الشعر والبشرة والأظافر.",
		[]size{sz(60, "tab", 1), sz(120, "tab", 1.8)}},

	// skin-care
	{"skin-care", "Intensive Moisturising Cream", "كريم مرطب مكثف", "glycerin, urea", false, 3, 34.00,
		"Fragrance-free cream for very dry skin.", "كريم خالٍ من العطور للبشرة شديدة الجفاف.",
		[]size{sz(100, "ml", 1), sz(250, "ml", 2.1)}},
	{"skin-care", "Sunscreen SPF 50+", "واقي شمس SPF 50+", "zinc oxide, titanium dioxide", false, 3, 69.00,
		"High protection, water-resistant, light texture.", "حماية عالية ومقاوم للماء بقوام خفيف.",
		[]size{sz(50, "ml", 1), sz(100, "ml", 1.7)}},
	{"skin-care", "Hydrocortisone 1% Cream", "كريم هيدروكورتيزون 1٪", "hydrocortisone", false, 10, 14.25,
		"Relieves itching, insect bites and mild eczema.", "يخفف الحكة ولدغات الحشرات والإكزيما الخفيفة.",
		[]size{sz(15, "g", 1), sz(30, "g", 1.7)}},
	{"skin-care", "Benzoyl Peroxide 5% Acne Gel", "جل حب الشباب بنزويل بيروكسايد 5٪", "benzoyl peroxide", false, 12, 27.50,
		"Treats and helps prevent spots.", "يعالج حب الشباب ويساعد على الوقاية منه.",
		[]size{sz(40, "g", 1)}},
	{"skin-care", "Clotrimazole 1% Cream", "كريم كلوتريمازول 1٪", "clotrimazole", false, 2, 12.00,
		"Treats fungal skin infections such as athlete's foot.", "يعالج الالتهابات الفطرية الجلدية مثل قدم الرياضي.",
		[]size{sz(20, "g", 1)}},
	{"skin-care", "Micellar Cleansing Water", "ماء ميسيلار منظف", "", false, 12, 39.00,
		"Removes make-up and impurities without rinsing.", "يزيل المكياج والشوائب دون الحاجة إلى الشطف.",
		[]size{sz(250, "ml", 1), sz(400, "ml", 1.4)}},

	// baby-care
	{"baby-care", "Nappy Rash Cream", "كريم التسلخات للأطفال", "zinc oxide", false, 0, 22.00,
		"Soothes and protects against nappy rash.", "يهدئ التسلخات ويحمي منها.",
		[]size{sz(50, "g", 1), sz(100, "g", 1.7)}},
	{"baby-care", "Baby Gentle Shampoo", "شامبو لطيف للأطفال", "", false, 0, 19.50,
		"Tear-free shampoo for delicate scalps.", "شامبو لا يسبب الدموع لفروة رأس الطفل الحساسة.",
		[]size{sz(200, "ml", 1), sz(500, "ml", 2.1)}},
	{"baby-care", "Infant Colic Drops", "قطرات المغص للرضع", "simethicone", false, 0, 21.75,
		"Relieves wind and colic in babies.", "تخفف الغازات والمغص عند الرضع.",
		[]size{sz(30, "ml", 1)}},
	{"baby-care", "Fragrance-Free Baby Wipes", "مناديل مبللة للأطفال بدون عطر", "", false, 0, 12.00,
		"99% water wipes for sensitive skin.", "مناديل مكونة من 99٪ ماء للبشرة الحساسة.",
		[]size{sz(60, "wipe", 1), sz(180, "wipe", 2.6)}},

	// oral-care
	{"oral-care", "Sensitive Fluoride Toothpaste", "معجون أسنان بالفلورايد للأسنان الحساسة", "potassium nitrate, sodium fluoride", false, 6, 16.00,
		"Daily protection for sensitive teeth.", "حماية يومية للأسنان الحساسة.",
		[]size{sz(75, "ml", 1), sz(100, "ml", 1.25)}},
	{"oral-care", "Chlorhexidine 0.12% Mouthwash", "غسول فم كلورهيكسيدين 0.12٪", "chlorhexidine", false, 12, 24.00,
		"Antiseptic mouthwash for gum care.", "غسول فم مطهر للعناية باللثة.",
		[]size{sz(250, "ml", 1), sz(500, "ml", 1.7)}},
	{"oral-care", "Kids Strawberry Toothpaste", "معجون أسنان للأطفال بنكهة الفراولة", "sodium fluoride", false, 2, 11.50,
		"Low-fluoride toothpaste for children aged 2 to 6.", "معجون منخفض الفلورايد للأطفال من 2 إلى 6 سنوات.",
		[]size{sz(50, "ml", 1)}},

	// first-aid
	{"first-aid", "Povidone-Iodine 10% Antiseptic Solution", "محلول مطهر بوفيدون يود 10٪", "povidone-iodine", false, 0, 13.50,
		"Cleans minor cuts and grazes.", "لتطهير الجروح والخدوش البسيطة.",
		[]size{sz(60, "ml", 1), sz(125, "ml", 1.7)}},
	{"first-aid", "Assorted Adhesive Bandages", "لاصقات جروح متنوعة", "", false, 0, 9.75,
		"Breathable plasters in assorted sizes.", "لاصقات جروح قابلة للتنفس بأحجام متنوعة.",
		[]size{sz(20, "pc", 1), sz(40, "pc", 1.7)}},
	{"first-aid", "Digital Thermometer", "ميزان حرارة رقمي", "", false, 0, 35.00,
		"Fast 10-second reading with fever alert.", "قراءة سريعة خلال 10 ثوانٍ مع تنبيه للحرارة.",
		[]size{sz(1, "pc", 1)}},

	// diabetes-care
	{"diabetes-care", "Blood Glucose Test Strips", "شرائط فحص سكر الدم", "", false, 0, 59.00,
		"Accurate blood sugar results in 5 seconds.", "نتائج دقيقة لسكر الدم خلال 5 ثوانٍ.",
		[]size{sz(25, "strip", 1), sz(50, "strip", 1.8)}},
	{"diabetes-care", "Sterile Lancets 30G", "إبر وخز معقمة 30G", "", false, 0, 18.00,
		"Ultra-thin lancets for comfortable testing.", "إبر رفيعة جداً لفحص مريح.",
		[]size{sz(100, "lancet", 1), sz(200, "lancet", 1.8)}},

	// prescription
	{"prescription", "Amoxicillin 500 mg Capsules", "كبسولات أموكسيسيلين 500 ملغ", "amoxicillin", true, 0, 14.00,
		"Antibiotic for bacterial infections.", "مضاد حيوي لعلاج الالتهابات البكتيرية.",
		[]size{sz(21, "cap", 1)}},
	{"prescription", "Azithromycin 250 mg Tablets", "أقراص أزيثروميسين 250 ملغ", "azithromycin", true, 0, 27.00,
		"Antibiotic for respiratory and skin infections.", "مضاد حيوي لالتهابات الجهاز التنفسي والجلد.",
		[]size{sz(6, "tab", 1)}},
	{"prescription", "Metformin 500 mg Tablets", "أقراص ميتفورمين 500 ملغ", "metformin", true, 18, 9.50,
		"Helps control blood sugar in type 2 diabetes.", "يساعد على ضبط سكر الدم لمرضى السكري من النوع الثاني.",
		[]size{sz(30, "tab", 1), sz(60, "tab", 1.8)}},
	{"prescription", "Atorvastatin 20 mg Tablets", "أقراص أتورفاستاتين 20 ملغ", "atorvastatin", true, 18, 32.00,
		"Lowers cholesterol.", "يخفض الكوليسترول.",
		[]size{sz(30, "tab", 1)}},
	{"prescription", "Amlodipine 5 mg Tablets", "أقراص أملوديبين 5 ملغ", "amlodipine", true, 18, 18.50,
		"Treats high blood pressure.", "يعالج ارتفاع ضغط الدم.",
		[]size{sz(30, "tab", 1)}},
	{"prescription", "Salbutamol 100 mcg Inhaler", "بخاخ سالبوتامول 100 ميكروغرام", "salbutamol", true, 4, 16.75,
		"Relieves asthma symptoms.", "يخفف أعراض الربو.",
		[]size{sz(200, "dose", 1)}},
	{"prescription", "Prednisolone 5 mg Tablets", "أقراص بريدنيزولون 5 ملغ", "prednisolone", true, 0, 8.75,
		"Corticosteroid for inflammation and allergic conditions.", "كورتيكوستيرويد لعلاج الالتهابات وحالات الحساسية.",
		[]size{sz(30, "tab", 1)}},
}

var catCode = map[string]string{
	"pain-relief": "PAIN", "cold-flu": "COLD", "allergy": "ALRG", "digestive": "DIGE",
	"vitamins": "VITA", "skin-care": "SKIN", "baby-care": "BABY", "oral-care": "ORAL",
	"first-aid": "FAID", "diabetes-care": "DIAB", "prescription": "RX",
}

func roundTo(sar, step float64) commerce.Money {
	return commerce.SAR(math.Round(sar/step) * step)
}

func date(s string) *time.Time {
	t, err := time.Parse(time.DateOnly, s)
	if err != nil {
		panic(err)
	}
	return &t
}

type gen struct {
	r        *rand.Rand
	products []commerce.Product
	seq      map[string]int
}

func (g *gen) add(t tmpl, b brand, s size, priceFactor float64) {
	cat := t.cat
	g.seq[cat]++
	sku := fmt.Sprintf("%s-%03d", catCode[cat], g.seq[cat])

	descEN := fmt.Sprintf("%s Pack size: %s.", t.descEN, s.en())
	descAR := fmt.Sprintf("%s حجم العبوة: %s.", t.descAR, s.ar())
	if t.rx {
		descEN += " Prescription-only medicine."
		descAR += " دواء يصرف بوصفة طبية فقط."
	}
	if t.age > 0 {
		descEN += fmt.Sprintf(" Not for children under %d.", t.age)
		descAR += fmt.Sprintf(" غير مناسب للأطفال دون %d سنة.", t.age)
	}

	p := commerce.Product{
		SKU:                  sku,
		Name:                 fmt.Sprintf("%s %s (%s)", b.short, t.en, s.en()),
		NameAr:               fmt.Sprintf("%s من %s (%s)", t.ar, b.ar, s.ar()),
		Description:          descEN,
		DescriptionAr:        descAR,
		Brand:                b.name,
		Category:             cat,
		Price:                roundTo(t.price*s.mult*priceFactor, 0.25),
		Status:               commerce.StatusEnabled,
		Stock:                g.stock(),
		RequiresPrescription: t.rx,
		ActiveIngredient:     t.ing,
		AgeMin:               t.age,
	}

	// About a fifth of OTC products are on sale.
	if !t.rx && g.r.Float64() < 0.22 {
		off := []float64{0.10, 0.15, 0.20, 0.25}[g.r.IntN(4)]
		sp := roundTo(p.Price.Riyals()*(1-off), 0.05)
		p.SpecialPrice = &sp
	}
	g.products = append(g.products, p)
}

func (g *gen) stock() map[commerce.City]int {
	m := make(map[commerce.City]int, len(commerce.Cities))
	soldOutEverywhere := g.r.Float64() < 0.03
	for _, c := range commerce.Cities {
		q := 5 + g.r.IntN(115)
		if soldOutEverywhere || g.r.Float64() < 0.12 {
			q = 0
		}
		m[c] = q
	}
	return m
}

func main() {
	out := flag.String("out", "data/products.json", "output file")
	flag.Parse()

	g := &gen{r: rand.New(rand.NewPCG(2026, 10)), seq: map[string]int{}}

	// Primary brand, every size.
	for _, t := range templates {
		for _, s := range t.sizes {
			g.add(t, categoryBrands[t.cat][0], s, 1)
		}
	}
	// Cheaper secondary-brand equivalents of the smallest pack, until target.
	for i := 0; len(g.products) < target; i++ {
		t := templates[i%len(templates)]
		g.add(t, categoryBrands[t.cat][1], t.sizes[0], 0.8)
	}
	if len(g.products) != target {
		log.Fatalf("generated %d products, want %d: trim templates", len(g.products), target)
	}

	// Fixed edge cases the tests and demo rely on.
	setSale := func(sku string, sar float64, from, to string) {
		for i := range g.products {
			if g.products[i].SKU == sku {
				sp := commerce.SAR(sar)
				g.products[i].SpecialPrice = &sp
				if from != "" {
					g.products[i].SpecialFrom = date(from)
				}
				if to != "" {
					g.products[i].SpecialTo = date(to)
				}
				return
			}
		}
		log.Fatalf("unknown sku %s", sku)
	}
	setSale("PAIN-001", 4.95, "2026-01-01", "2027-12-31")  // active window
	setSale("VITA-001", 19.95, "2026-01-01", "2026-02-01") // expired: full price
	setSale("SKIN-003", 49.00, "2027-06-01", "")           // future: full price
	for _, sku := range []string{"COLD-005", "VITA-013", "SKIN-010"} {
		for i := range g.products {
			if g.products[i].SKU == sku {
				g.products[i].Status = commerce.StatusDisabled
			}
		}
	}

	data, err := json.MarshalIndent(g.products, "", "  ")
	if err != nil {
		log.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(*out), 0o755); err != nil {
		log.Fatal(err)
	}
	if err := os.WriteFile(*out, append(data, '\n'), 0o644); err != nil {
		log.Fatal(err)
	}
	fmt.Printf("wrote %d products to %s\n", len(g.products), *out)
}
