import type { App } from "vue";
import { createI18n } from "vue-i18n";
import { localStg } from "@/utils/storage";
import messages from "./locale";

// Legacy stored preference "zh-CN" resolves to Traditional Chinese now.
const storedLang = localStg.get("lang");
const initialLang = (storedLang === "zh-CN" ? "zh-TW" : storedLang || "zh-TW") as App.I18n.LangType;

const i18n = createI18n({
  locale: initialLang,
  fallbackLocale: "en",
  messages,
  legacy: false,
});

/**
 * Setup plugin i18n
 *
 * @param app
 */
export function setupI18n(app: App) {
  app.use(i18n);
}

export const $t = i18n.global.t as App.I18n.$T;

export function setLocale(locale: App.I18n.LangType) {
  i18n.global.locale.value = locale;

  document?.querySelector("html")?.setAttribute("lang", locale);
}

export function getLocale(): App.I18n.LangType {
  return i18n.global.locale.value as App.I18n.LangType;
}
