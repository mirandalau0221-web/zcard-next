import { dateEnUS, dateZhCN, dateZhTW, enUS, zhCN, zhTW } from "naive-ui";
import type { NDateLocale, NLocale } from "naive-ui";

export const naiveLocales: Record<App.I18n.LangType, NLocale> = {
  "zh-CN": zhCN,
  "zh-TW": zhTW,
  "en-US": enUS,
};

export const naiveDateLocales: Record<App.I18n.LangType, NDateLocale> = {
  "zh-CN": dateZhCN,
  "zh-TW": dateZhTW,
  "en-US": dateEnUS,
};
