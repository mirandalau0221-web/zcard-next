<script setup lang="ts">
import AccountSecurity from "@/components/security/account-security.vue";
// 員工與權限一體化管理（大廠控制枱模式）：員工為主軸、角色內聚為抽屜——
// 行內「權限」直達該員工角色的權限配置，無需獨立角色頁。
import { ref, reactive, computed, onMounted, h } from "vue";
import {
  NButton,
  NSwitch,
  NTag,
  NDropdown,
  NSpace,
} from "naive-ui";
import type { DataTableColumns, DropdownOption } from "naive-ui";
import {
  fetchAdmins,
  createAdmin,
  updateAdmin,
  toggleAdmin,
  deleteAdmin,
  resetAdminPassword,
  fetchRoles,
  fetchPermissionTree,
} from "@/service/api";
import { checkAuth } from "@/directives";
import { hasRoutePermission } from "@/router/permissions";
import { useAuthStore } from "@/store/modules/auth";
import { useRouteStore } from "@/store/modules/route";
import { useRoute, useRouter } from "vue-router";
import RoleManageDrawer from "./components/role-manage-drawer.vue";
import PermsDrawer from "./components/perms-drawer.vue";
import FilterTabs from "@/components/common/filter-tabs.vue";

defineOptions({ name: "StaffManagement" });

const route = useRoute();
const router = useRouter();
const authStore = useAuthStore();
const routeStore = useRouteStore();

const loading = ref(false);
const saving = ref(false);
const showCreate = ref(false);
const showEdit = ref(false);
const admins = ref<any[]>([]);
const roles = ref<any[]>([]);
const permTree = ref<any[]>([]);
const editing = ref<any>(null);

// 啓用狀態快捷篩選（客户端過濾——員工列表全量加載無分頁，帶實時計數）
const enabledFilter = ref<"" | "on" | "off">("");
const enabledTabs = [
  { label: "全部", value: "", type: "default" as const },
  { label: "已啓用", value: "on", type: "success" as const },
  { label: "已禁用", value: "off", type: "error" as const },
];
const enabledCounts = computed(() => ({
  "": admins.value.length,
  on: admins.value.filter((a) => a.enabled).length,
  off: admins.value.filter((a) => !a.enabled).length,
}));
const filteredAdmins = computed(() =>
  enabledFilter.value === "" ? admins.value : admins.value.filter((a) => (enabledFilter.value === "on" ? a.enabled : !a.enabled)),
);

// 角色管理抽屜 / 權限配置抽屜
const showRoleDrawer = ref(false);
const showPermsDrawer = ref(false);
const permsRole = ref<any>(null);

const form = reactive({ username: "", password: "", nickname: "", role_id: null as number | null });
const editForm = reactive({ nickname: "", role_id: null as number | null });

// 重置密碼
const showResetPwd = ref(false);
const resettingPwd = ref(false);
const resetPwdTarget = ref<any>(null);
const resetPwdForm = reactive({ password: "" });

const roleMap = computed(() => new Map(roles.value.map((r) => [r.id, r])));
const roleOptions = computed(() => roles.value.map((r) => ({ label: r.name, value: r.id })));
const memberCount = computed(() => {
  const counts = new Map<number, number>();
  admins.value.forEach((a) => counts.set(a.role_id, (counts.get(a.role_id) || 0) + 1));
  return counts;
});

// 是否內置超級管理員（系統賬號：不可刪除，列表打「系統」標）
function isSuper(row: any) {
  return roleMap.value.get(row.role_id)?.code === "super_admin";
}

const columns: DataTableColumns<any> = [
  { title: "ID", key: "id", width: 50 },
  {
    title: "用户名",
    key: "username",
    width: 150,
    render: (row) =>
      isSuper(row)
        ? h(
            NSpace,
            { size: 6, wrap: false, align: "center" },
            {
              default: () => [
                h("span", {}, row.username),
                h(NTag, { type: "info", size: "small", bordered: false }, { default: () => "系統" }),
              ],
            },
          )
        : row.username,
  },
  { title: "暱稱", key: "nickname", width: 110 },
  {
    title: "角色",
    key: "role",
    width: 110,
    render: (row) => {
      const role = roleMap.value.get(row.role_id);
      return role
        ? h(
            NTag,
            { size: "small", type: isSuper(row) ? "primary" : "default", bordered: !isSuper(row) },
            { default: () => role.name },
          )
        : "-";
    },
  },
  {
    title: "TOTP",
    key: "totp_enabled",
    width: 64,
    render: (row) => (row.totp_enabled ? "✓" : "-"),
  },
  {
    title: "啓用",
    key: "enabled",
    width: 72,
    render: (row) =>
      checkAuth("identity:admin_toggle")
        ? h(NSwitch, {
            value: row.enabled,
            onUpdateValue: (val: boolean) => handleToggle(row, val),
          })
        : row.enabled
          ? "✓"
          : "-",
  },
  {
    title: "操作",
    key: "actions",
    width: 300,
    render: (row) =>
      h(
        NSpace,
        { size: 4, wrap: false },
        {
          default: () => [
            checkAuth("authz:role_grant")
              ? h(
                  NButton,
                  { size: "small", type: "primary", secondary: true, onClick: () => openPerms(row) },
                  { default: () => "權限" },
                )
              : null,
            checkAuth("identity:admin_write")
              ? h(
                  NButton,
                  { size: "small", onClick: () => openEdit(row) },
                  { default: () => "編輯" },
                )
              : null,
            checkAuth("identity:admin_reset_pwd")
              ? h(
                  NButton,
                  { size: "small", onClick: () => openResetPwd(row) },
                  { default: () => "重置密碼" },
                )
              : null,
            moreMenu(row),
          ],
        },
      ),
  },
];

// 次要操作收斂為「更多」下拉（大廠表格慣例：主操作外露，次操作收進 ⋯）
function moreMenu(row: any) {
  const options: DropdownOption[] = [];
  if (row.totp_enabled && checkAuth("identity:admin_totp_reset")) {
    options.push({ label: "解綁 TOTP", key: "totp-reset" });
  }
  if (!isSuper(row) && checkAuth("identity:admin_delete")) {
    options.push({ label: "刪除員工", key: "delete" });
  }
  if (!options.length) return null;
  return h(
    NDropdown,
    {
      options,
      trigger: "click",
      onSelect: (key: string | number) => handleMore(key, row),
    },
    { default: () => h(NButton, { size: "small", quaternary: true }, { default: () => "⋯" }) },
  );
}

function handleMore(key: string | number, row: any) {
  switch (key) {
    case "totp-reset":
      handleResetTOTP(row);
      break;
    case "delete":
      handleDelete(row);
      break;
  }
}

async function loadList() {
  loading.value = true;
  try {
    // 先角色後員工：roleMap/super 判定依賴 roles 已加載
    const roleRes = await fetchRoles();
    if (!roleRes.error && roleRes.data) {
      roles.value = (roleRes.data as any).roles || [];
    }
    const adminRes = await fetchAdmins();
    if (!adminRes.error && adminRes.data) {
      admins.value = (adminRes.data as any).admins || [];
    }
  } finally {
    loading.value = false;
  }
}

async function loadPermTree() {
  const { data, error } = await fetchPermissionTree();
  if (!error && data) {
    const groups = (data as any).groups || [];
    permTree.value = groups.map((g: any) => ({
      key: `group:${g.domain}`,
      label: g.label || g.domain,
      children: (g.perms || []).map((p: any) => ({
        key: p.code,
        label: `${p.desc}（${p.code}）`,
      })),
    }));
  }
}

// ── 權限抽屜（行內入口：編輯該員工所屬角色的權限）──────
function openPerms(row: any) {
  const role = roleMap.value.get(row.role_id);
  if (!role) return;
  permsRole.value = role;
  showPermsDrawer.value = true;
}

// 權限保存後：刷新當前登錄者權限點 + 按新權限重建路由/菜單（免手動刷新頁面）。
// 後端 admin 鑑權每請求實時按庫中 RoleID 判定，此處僅同步前端表現層。
async function handlePermsSaved() {
  await loadList();
  await authStore.initUserInfo(); // 重新拉取 profile.permissions（後端實時下發）
  await routeStore.initAuthRoute(); // 重建 authRoutes/菜單（含失去權限頁面的移除）
  if (!hasRoutePermission(String(route.name || ""), authStore.userInfo.buttons)) {
    // 當前頁面權限已被收回 → 跳轉到新的首頁（initAuthRoute 已重算 routeHome）
    window.$message?.warning("當前頁面權限已變更，已自動跳轉");
    router.push(routeStore.routeHome);
  } else {
    window.$message?.success("權限已更新，菜單已同步刷新");
  }
}

// ── 員工 CRUD ──────────────────────────────────
function openEdit(row: any) {
  editing.value = row;
  editForm.nickname = row.nickname || "";
  editForm.role_id = row.role_id;
  showEdit.value = true;
}

async function handleEdit() {
  if (!editing.value || !editForm.role_id) return;
  saving.value = true;
  try {
    const { error } = await updateAdmin(editing.value.id, {
      nickname: editForm.nickname,
      role_id: editForm.role_id,
    });
    if (!error) {
      window.$message?.success("已保存（角色變更即時生效）");
      showEdit.value = false;
      loadList();
    }
  } finally {
    saving.value = false;
  }
}

async function handleCreate() {
  if (!form.username || !form.password || !form.role_id) return;
  saving.value = true;
  try {
    const { error } = await createAdmin({ ...form, role_id: form.role_id as number });
    if (!error) {
      window.$message?.success("創建成功");
      showCreate.value = false;
      Object.assign(form, { username: "", password: "", nickname: "", role_id: null });
      loadList();
    }
  } finally {
    saving.value = false;
  }
}

async function handleToggle(row: any, val: boolean) {
  const { error } = await toggleAdmin(row.id, val);
  if (!error) {
    row.enabled = val;
    window.$message?.success(val ? "已啓用" : "已停用（其在線會話即時失效）");
  } else {
    loadList(); // 末位超管等後端拒絕場景：回讀真實狀態
  }
}

async function handleDelete(row: any) {
  const { error } = await deleteAdmin(row.id);
  if (!error) {
    window.$message?.success(`已刪除員工「${row.username}」（其全部後台會話已清除）`);
    loadList();
  }
}

function openResetPwd(row: any) {
  resetPwdTarget.value = row;
  resetPwdForm.password = "";
  showResetPwd.value = true;
}

async function handleResetPwd() {
  if (!resetPwdTarget.value || resetPwdForm.password.length < 8) return;
  resettingPwd.value = true;
  try {
    const { error } = await resetAdminPassword(resetPwdTarget.value.id, resetPwdForm.password);
    if (!error) {
      window.$message?.success("密碼已重置（該員工全部後台會話已登出）");
      showResetPwd.value = false;
    }
  } finally {
    resettingPwd.value = false;
  }
}

const resetTotpTarget = ref<any>(null);
function handleResetTOTP(row: any) { resetTotpTarget.value = row; }
function finishResetTotp() { resetTotpTarget.value = null; loadList(); }


onMounted(() => {
  loadList();
  loadPermTree();
});
</script>

<template>
  <div class="min-h-500px">
    <!-- 路由動畫要求單個根元素，彈窗與頁面內容必須放在同一容器內。 -->
    <NModal :show="Boolean(resetTotpTarget)" preset="card" title="重置兩步驗證" style="width:620px;max-width:calc(100vw - 24px)" @update:show="value => { if (!value) resetTotpTarget = null; }"><AccountSecurity v-if="resetTotpTarget" :reset-target="resetTotpTarget" @completed="finishResetTotp" /></NModal>
    <NCard title="員工管理">
      <!-- 頂欄：主操作「新增員工」+ 角色入口（權限表現層收斂於此頁） -->
      <div class="mb-16px flex items-center gap-12px">
        <NButton v-auth="'identity:admin_write'" type="primary" @click="showCreate = true">
          新增員工
        </NButton>
        <NButton v-auth="'authz:role_read'" @click="showRoleDrawer = true"> 角色管理 </NButton>
        <span class="ml-auto text-12px text-gray-400">
          行內「權限」= 修改該員工所屬角色的權限點（對同角色所有人生效）
        </span>
      </div>
      <FilterTabs v-model:value="enabledFilter" :options="enabledTabs" :counts="enabledCounts" class="mb-12px" />
      <NDataTable :columns="columns" :data="filteredAdmins" :loading="loading" :row-key="(r: any) => r.id" :max-height="540" />
    </NCard>

    <!-- 新增員工 -->
    <NModal v-model:show="showCreate" preset="dialog" title="新增員工" style="width: 480px">
      <NForm :model="form" label-placement="left" label-width="80">
        <NFormItem label="用户名" required>
          <NInput v-model:value="form.username" />
        </NFormItem>
        <NFormItem label="密碼" required>
          <NInput
            v-model:value="form.password"
            type="password"
            show-password-on="click"
            placeholder="至少 8 位"
          />
        </NFormItem>
        <NFormItem label="暱稱">
          <NInput v-model:value="form.nickname" />
        </NFormItem>
        <NFormItem label="角色" required>
          <NSelect v-model:value="form.role_id" :options="roleOptions" />
        </NFormItem>
      </NForm>
      <template #action>
        <NButton @click="showCreate = false">取消</NButton>
        <NButton type="primary" :loading="saving" @click="handleCreate">創建</NButton>
      </template>
    </NModal>

    <!-- 編輯員工（暱稱/角色；改角色即時生效——後端中間件按庫中當前 RoleID 判權） -->
    <NModal
      v-model:show="showEdit"
      preset="dialog"
      :title="`編輯員工：${editing?.username || ''}`"
      style="width: 440px"
    >
      <NForm :model="editForm" label-placement="left" label-width="80">
        <NFormItem label="暱稱">
          <NInput v-model:value="editForm.nickname" />
        </NFormItem>
        <NFormItem label="角色" required>
          <NSelect v-model:value="editForm.role_id" :options="roleOptions" />
        </NFormItem>
      </NForm>
      <template #action>
        <NButton @click="showEdit = false">取消</NButton>
        <NButton type="primary" :loading="saving" @click="handleEdit">保存</NButton>
      </template>
    </NModal>

    <!-- 重置密碼（高危：吊銷該員工全部後台會話強制重登） -->
    <NModal
      v-model:show="showResetPwd"
      preset="dialog"
      :title="`重置密碼：${resetPwdTarget?.username || ''}`"
      style="width: 440px"
    >
      <NForm :model="resetPwdForm" label-placement="left" label-width="80">
        <NFormItem label="新密碼" required>
          <NInput
            v-model:value="resetPwdForm.password"
            type="password"
            show-password-on="click"
            placeholder="至少 8 位；重置後該員工全部後台會話立即登出"
          />
        </NFormItem>
      </NForm>
      <template #action>
        <NButton @click="showResetPwd = false">取消</NButton>
        <NButton
          v-auth="'identity:admin_reset_pwd'"
          type="primary"
          :loading="resettingPwd"
          :disabled="resetPwdForm.password.length < 8"
          @click="handleResetPwd"
        >
          重置
        </NButton>
      </template>
    </NModal>

    <!-- 角色管理抽屜（新建/編輯/刪除角色 + 權限入口） -->
    <RoleManageDrawer
      v-model:show="showRoleDrawer"
      :roles="roles"
      :member-count="memberCount"
      @refresh="loadList"
      @open-perms="(r: any) => ((permsRole = r), (showPermsDrawer = true))"
    />

    <!-- 權限配置抽屜（行內「權限」與角色抽屜共用） -->
    <PermsDrawer
      v-model:show="showPermsDrawer"
      :role="permsRole"
      :perm-tree="permTree"
      @saved="handlePermsSaved"
    />
  </div>
</template>
