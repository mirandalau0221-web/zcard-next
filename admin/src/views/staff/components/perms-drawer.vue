<script setup lang="ts">
// 權限配置抽屜：按角色編輯權限點（員工頁行內「權限」與角色管理抽屜共用）。
// 頭部操作條：全選/反選 + 展開全部/摺疊全部；超管（* 通配）打開時默認全勾，
// 保存後轉為精確權限清單——取消勾選的功能即刻不可訪問。
import { computed, ref, watch } from "vue";
import { NDrawer, NDrawerContent, NTree, NButton, NAlert, NSpace } from "naive-ui";
import { updateRolePermissions } from "@/service/api";

defineOptions({ name: "RolePermsDrawer" });

const props = defineProps<{
  show: boolean;
  role: any;
  permTree: any[];
}>();

const emit = defineEmits(["update:show", "saved"]);

const checked = ref<string[]>([]);
const expanded = ref<string[]>([]);
const saving = ref(false);

// 全量葉子權限碼（勾選模型只存葉子碼，父級勾選態由 cascade 自動推導）
const allCodes = computed<string[]>(() =>
  props.permTree.flatMap((g: any) => (g.children || []).map((c: any) => c.key as string)),
);
const groupKeys = computed<string[]>(() => props.permTree.map((g: any) => g.key as string));
const isWildcard = computed(() => (props.role?.permissions || []).includes("*"));
// cascade 勾選時 naive-ui 會把父級分組鍵一併寫入 checked-keys——
// 保存與計數必須過濾出葉子權限碼，否則分組鍵會作為權限點落庫
const allCodeSet = computed<Set<string>>(() => new Set(allCodes.value));
const leafChecked = computed<string[]>(() => checked.value.filter((c) => allCodeSet.value.has(c)));
const allChecked = computed(() => leafChecked.value.length === allCodes.value.length);

watch(
  () => props.show,
  (v) => {
    if (!v) return;
    // 超管 * = 全部權限：打開即全勾（保存轉為精確清單）；普通角色按已保存清單回顯
    checked.value = isWildcard.value
      ? [...allCodes.value]
      : (props.role?.permissions || []).filter((p: string) => p !== "*");
    expanded.value = [...groupKeys.value]; // 默認展開全部
  },
);

function selectAll() {
  checked.value = [...allCodes.value];
}

function invertSelection() {
  checked.value = allCodes.value.filter((c) => !leafChecked.value.includes(c));
}

function expandAll() {
  expanded.value = [...groupKeys.value];
}

function collapseAll() {
  expanded.value = [];
}

async function handleSave() {
  if (!props.role) return;
  if (!leafChecked.value.length) {
    window.$message?.error("至少保留一項權限——清空將導致該角色（含你自己）失去後台訪問能力");
    return;
  }
  saving.value = true;
  try {
    const { error } = await updateRolePermissions(props.role.id, leafChecked.value);
    if (!error) {
      // 成功提示由父組件 handlePermsSaved 統一反饋（含跳轉場景），避免雙彈窗
      emit("saved");
      emit("update:show", false);
    }
  } finally {
    saving.value = false;
  }
}
</script>

<template>
  <NDrawer
    :show="show"
    :width="440"
    :auto-focus="false"
    :close-on-esc="false"
    @update:show="(v: boolean) => emit('update:show', v)"
  >
    <NDrawerContent :title="`權限配置：${role?.name || ''}`" closable>
      <NAlert :type="isWildcard ? 'warning' : 'info'" :bordered="false" class="mb-12px">
        <template v-if="isWildcard">
          該角色當前為「全部權限（* 通配）」，保存後將轉為<b>精確權限清單</b>——取消勾選的功能即刻不可訪問。
        </template>
        <template v-else>
          正在修改角色「{{ role?.name || "" }}」的權限，對該角色下<b>所有員工</b>生效。
        </template>
      </NAlert>

      <!-- 頭部操作條：批量勾選 + 樹形態 -->
      <div class="mb-8px flex items-center gap-8px">
        <NSpace size="small">
          <NButton size="tiny" secondary @click="selectAll">全選</NButton>
          <NButton size="tiny" secondary @click="invertSelection">反選</NButton>
        </NSpace>
        <span class="text-12px text-gray-400">
          已選 {{ leafChecked.length }}/{{ allCodes.length }}
        </span>
        <NSpace size="small" class="ml-auto">
          <NButton size="tiny" quaternary @click="expandAll">展開全部</NButton>
          <NButton size="tiny" quaternary @click="collapseAll">摺疊全部</NButton>
        </NSpace>
      </div>

      <NTree
        v-model:checked-keys="checked"
        v-model:expanded-keys="expanded"
        :data="permTree"
        checkable
        cascade
        selectable
        block-line
        key-field="key"
        label-field="label"
        children-field="children"
        class="max-h-540px overflow-auto"
      />
      <template #footer>
        <div class="flex justify-end gap-8px">
          <NButton @click="emit('update:show', false)">取消</NButton>
          <NButton
            v-auth="'authz:role_grant'"
            type="primary"
            :loading="saving"
            @click="handleSave"
          >
            保存{{ allChecked ? "" : `（${leafChecked.length} 項）` }}
          </NButton>
        </div>
      </template>
    </NDrawerContent>
  </NDrawer>
</template>
