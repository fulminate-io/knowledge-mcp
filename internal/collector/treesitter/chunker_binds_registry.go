// SPDX-License-Identifier: Apache-2.0

package treesitter

// languagesWithBindsArms is the set chunker_binds.go registers. It lives beside
// the unregister helper rather than in that file because the registration count
// there is pinned by a gate that greps the registrar's own name, and an
// Unregister call spells that name as a substring.
var languagesWithBindsArms = []Language{
	LangJava, LangKotlin, LangScala, LangPython,
	LangRust, LangSwift, LangCSharp, LangPHP,
	LangC, LangCPP,
}

// UnregisterLanguageBindsResolvers removes every arm chunker_binds.go installs.
//
// IT EXISTS FOR EXACTLY ONE CALLER: the corpus verification's arm-off baseline,
// which measures the pre-arm world and writes it to a separate artifact. It is
// deliberately NOT a general test affordance — a test that wants a language's
// arm out of the way for one case should install its own probe and RESTORE
// through RegisterLanguageBindsResolvers, because deleting an entry disarms the
// language for every later test in the same binary.
func UnregisterLanguageBindsResolvers() {
	for _, lang := range languagesWithBindsArms {
		UnregisterBindsResolver(lang)
	}
}
