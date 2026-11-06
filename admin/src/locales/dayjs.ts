import { locale } from "dayjs";
import "dayjs/locale/zh-cn";
import "dayjs/locale/zh-tw";
import "dayjs/locale/en";
import { localStg } from "@/utils/storage";

/**
 * Set dayjs locale
 *
 * @param lang
 */
export function setDayjsLocale(lang: App.I18n.LangType = "zh-TW") {
  const localMap = {
    "zh-CN": "zh-cn",
    "zh-TW": "zh-tw",
    "en-US": "en",
  } satisfies Record<App.I18n.LangType, string>;

  const l = lang || localStg.get("lang") || "zh-TW";

  locale(localMap[l]);
}
