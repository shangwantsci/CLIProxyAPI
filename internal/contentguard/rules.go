package contentguard

import (
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

type strengthLevel int

const (
	strengthOff strengthLevel = iota
	strengthLoose
	strengthStandard
	strengthStrict
)

type compiledRule struct {
	category    string
	minStrength strengthLevel
	gates       []string
}

var ruleCatalog []compiledRule

func init() {
	// Gates are high-specificity phrases in English plus Simplified/Traditional Chinese.
	// Bare common words (自杀, 色情, 炸弹) are forbidden: they false-hit help-seeking and news.
	ruleCatalog = []compiledRule{
		{config.ContentGuardCategoryChildSafety, strengthLoose, []string{
			"child pornography",
			"child porn",
			"underage porn",
			"儿童色情",
			"兒童色情",
			"未成年色情",
			"幼童色情",
		}},
		{config.ContentGuardCategoryChildSafety, strengthStandard, []string{
			"csam",
			"sexualize a minor",
			"sex with a minor",
			"和未成年发生性关系",
			"和未成年發生性關係",
		}},
		{config.ContentGuardCategoryChildSafety, strengthStrict, []string{
			"underage sex",
			"minor pornography",
			"和儿童发生性关系",
			"和兒童發生性關係",
		}},

		{config.ContentGuardCategorySelfHarmInstructions, strengthLoose, []string{
			"how to kill myself",
			"how to commit suicide",
			"怎么杀死自己",
			"如何杀死自己",
			"怎麼殺死自己",
			"如何殺死自己",
			"自杀的具体步骤",
			"自殺的具體步驟",
		}},
		{config.ContentGuardCategorySelfHarmInstructions, strengthStandard, []string{
			"suicide method",
			"painless suicide",
			"无痛自杀方法",
			"無痛自殺方法",
		}},
		{config.ContentGuardCategorySelfHarmInstructions, strengthStrict, []string{
			"how to overdose to die",
			"怎么过量服药致死",
			"怎麼過量服藥致死",
		}},

		{config.ContentGuardCategoryWeaponsCBRNE, strengthLoose, []string{
			"how to make a bomb",
			"how to build a bomb",
			"怎么做炸弹",
			"怎么制造炸弹",
			"如何制造炸弹",
			"怎麼做炸彈",
			"如何製造炸彈",
		}},
		{config.ContentGuardCategoryWeaponsCBRNE, strengthStandard, []string{
			"synthesize sarin",
			"how to make ricin",
			"怎么合成沙林",
			"怎麼合成沙林",
		}},
		{config.ContentGuardCategoryWeaponsCBRNE, strengthStrict, []string{
			"enrich uranium for a weapon",
			"浓缩铀用于武器",
			"濃縮鈾用於武器",
		}},

		{config.ContentGuardCategoryViolentExtremism, strengthLoose, []string{
			"how to join isis",
			"怎么加入伊斯兰国",
			"怎麼加入伊斯蘭國",
		}},
		{config.ContentGuardCategoryViolentExtremism, strengthStandard, []string{
			"how to build a dirty bomb",
			"怎么做脏弹",
			"怎麼做髒彈",
		}},
		{config.ContentGuardCategoryViolentExtremism, strengthStrict, []string{
			"write a terrorist attack plan",
			"写一份恐怖袭击计划",
			"寫一份恐怖襲擊計劃",
		}},

		{config.ContentGuardCategoryCyberOffense, strengthLoose, []string{
			"write ransomware",
			"create ransomware",
			"写一个勒索软件",
			"写勒索软件",
			"寫一個勒索軟體",
		}},
		{config.ContentGuardCategoryCyberOffense, strengthStandard, []string{
			"write a keylogger",
			"build a botnet",
			"写一个键盘记录器",
			"写一个僵尸网络",
			"寫一個鍵盤記錄器",
		}},
		{config.ContentGuardCategoryCyberOffense, strengthStrict, []string{
			"write an exploit kit",
			"写一个漏洞利用工具包",
			"寫一個漏洞利用工具包",
		}},

		{config.ContentGuardCategoryFraud, strengthLoose, []string{
			"write a phishing email",
			"create a phishing page",
			"写一封钓鱼邮件",
			"制作钓鱼网页",
			"寫一封釣魚郵件",
		}},
		{config.ContentGuardCategoryFraud, strengthStandard, []string{
			"make a fake passport",
			"伪造护照",
			"偽造護照",
		}},
		{config.ContentGuardCategoryFraud, strengthStrict, []string{
			"generate a fake id card",
			"生成一张假身份证",
			"生成一張假身份證",
		}},

		{config.ContentGuardCategorySexualViolence, strengthLoose, []string{
			"write a rape fantasy",
			"写一个强奸幻想",
			"寫一個強姦幻想",
		}},
		{config.ContentGuardCategorySexualViolence, strengthStandard, []string{
			"non-consensual porn",
		}},

		{config.ContentGuardCategoryJailbreak, strengthLoose, []string{
			"ignore all previous instructions",
			"忽略之前的所有指令",
			"忽略以上所有指令",
		}},
		{config.ContentGuardCategoryJailbreak, strengthStandard, []string{
			"ignore previous instructions",
			"忽略之前的指令",
		}},
		{config.ContentGuardCategoryJailbreak, strengthStrict, []string{
			"jailbreak this model",
			"越狱这个模型",
			"越獄這個模型",
		}},

		{config.ContentGuardCategoryAdultSexual, strengthLoose, []string{
			"write an erotic story",
			"写一篇色情小说",
			"寫一篇色情小說",
		}},
		{config.ContentGuardCategoryAdultSexual, strengthStandard, []string{
			"erotic roleplay",
			"色情角色扮演",
		}},
		{config.ContentGuardCategoryAdultSexual, strengthStrict, []string{
			"sexually explicit chat",
		}},
	}
}

func parseStrength(value string) strengthLevel {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case config.ContentGuardStrengthLoose:
		return strengthLoose
	case config.ContentGuardStrengthStandard:
		return strengthStandard
	case config.ContentGuardStrengthStrict:
		return strengthStrict
	default:
		return strengthOff
	}
}

func strengthName(level strengthLevel) string {
	switch level {
	case strengthLoose:
		return config.ContentGuardStrengthLoose
	case strengthStandard:
		return config.ContentGuardStrengthStandard
	case strengthStrict:
		return config.ContentGuardStrengthStrict
	default:
		return config.ContentGuardStrengthOff
	}
}
