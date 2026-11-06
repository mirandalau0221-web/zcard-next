<script setup lang="ts">
// 角色管理抽屜（集成於員工管理頁）：角色列表 + 新建/編輯/刪除 + 行內權限入口。
import { h, reactive, ref } from "vue";
import {
  NDrawer,
  NDrawerContent,
  NDataTable,
  NButton,
  NTag,
  NForm,
  NFormItem,
  NInput,
  NModal,
  NPopconfirm,
  NSpace,
} from "naive-ui";
import type { DataTableColumns } from "naive-ui";
import { createRole, updateRole, deleteRole } from "@/service/api";
import { checkAuth } from "@/directives";

defineOptions({ name: "RoleManageDrawer" });

const props = defineProps<{
  show: boolean;
  roles: any[];
  memberCount: Map<number, number>;
}>();

const emit = defineEmits(["update:show", "refresh", "open-perms"]);

const showCreate = ref(false);
const saving = ref(false);
const createForm = reactive({ name: "", code: "", description: "" });

const showEdit = ref(false);
const editing = ref<any>(null);
const editForm = reactive({ name: "", description: "" });

const columns: DataTableColumns<any> = [
  {
    title: "角色",
    key: "name",
    width: 120,
    render: (row) =>
      row.is_builtin
        ? row.name + "　"
        : row.name,
  },
  {
    title: "內置",
    key: "is_builtin",
    width: 56,
    render: (row) =>
      row.is_builtin
        ? h(NTag, { type: "info", size: "small", bordered: false }, { default: () => "內置" })
        : "",
  },
  { title: "編碼", key: "code", width: 100 },
  {
    title: "成員",
    key: "member_count",
    width: 56,
    render: (row) => props.memberCount.get(row.id) || 0,
  },
  {
    title: "權限數",
    key: "perm_count",
    width: 64,
    render: (row) => (row.permissions || []).length,
  },
  {
    title: "操作",
    key: "actions",
    width: 168,
    render: (row) =>
      h(
        NSpace,
        { size: "small" },
        {
          default: () => [
            checkAuth("authz:role_grant")
              ? h(
                  NButton,
                  { size: "tiny", type: "primary", secondary: true, onClick: () => emit("open-perms", row) },
                  { default: () => "權限" },
                )
              : null,
            checkAuth("authz:role_write")
              ? h(
                  NButton,
                  { size: "tiny", onClick: () => openEdit(row) },
                  { default: () => "編輯" },
                )
              : null,
            !row.is_builtin && checkAuth("authz:role_delete")
              ? h(
                  NPopconfirm,
                  { onPositiveClick: () => handleDelete(row.id) },
                  {
                    trigger: () =>
                      h(NButton, { size: "tiny", type: "error", quaternary: true }, { default: () => "刪除" }),
                    default: () => "仍有員工掛載時會拒絕，確定刪除？",
                  },
                )
              : null,
          ],
        },
      ),
  },
];


function openEdit(row: any) {
  editing.value = row;
  editForm.name = row.name || "";
  editForm.description = row.description || "";
  showEdit.value = true;
}

async function handleCreate() {
  if (!createForm.name || !createForm.code) return;
  saving.value = true;
  try {
    const { error } = await createRole({ ...createForm });
    if (!error) {
      window.$message?.success("角色已創建，點擊「權限」為其分配權限點");
      showCreate.value = false;
      Object.assign(createForm, { name: "", code: "", description: "" });
      emit("refresh");
    }
  } finally {
    saving.value = false;
  }
}

async function handleEdit() {
  if (!editing.value || !editForm.name) return;
  saving.value = true;
  try {
    const { error } = await updateRole(editing.value.id, {
      name: editForm.name,
      description: editForm.description,
    });
    if (!error) {
      window.$message?.success("已保存");
      showEdit.value = false;
      emit("refresh");
    }
  } finally {
    saving.value = false;
  }
}

async function handleDelete(id: number) {
  const { error } = await deleteRole(id);
  if (!error) {
    window.$message?.success("刪除成功");
    emit("refresh");
  } else {
    window.$message?.error("刪除失敗（可能仍有員工掛載，請先在員工列表改派角色）");
  }
}
</script>

<template>
  <NDrawer
    :show="show"
    :width="640"
    :auto-focus="false"
    @update:show="(v: boolean) => emit('update:show', v)"
  >
    <NDrawerContent title="角色管理" closable>
      <div class="mb-12px flex items-center justify-between">
        <span class="text-13px text-gray-500">
          角色是權限的載體：新建角色後點「權限」分配權限點，再到員工列表掛派。
        </span>
        <NButton
          v-if="checkAuth('authz:role_write')"
          size="small"
          type="primary"
          @click="showCreate = !showCreate"
        >
          {{ showCreate ? "收起" : "新建角色" }}
        </NButton>
      </div>

      <!-- 新建（展開式表單） -->
      <div v-if="showCreate" class="mb-12px rounded-6px bg-gray-50 p-12px dark:bg-gray-800">
        <NForm
          :model="createForm"
          label-placement="left"
          label-width="56"
          size="small"
          inline
          :show-feedback="false"
        >
          <NFormItem label="名稱" required>
            <NInput v-model:value="createForm.name" placeholder="如：運營" class="w-120px" />
          </NFormItem>
          <NFormItem label="編碼" required>
            <NInput v-model:value="createForm.code" placeholder="如：operator" class="w-130px" />
          </NFormItem>
          <NFormItem label="描述">
            <NInput v-model:value="createForm.description" placeholder="可選" class="w-160px" />
          </NFormItem>
          <NButton type="primary" size="small" :loading="saving" @click="handleCreate">創建</NButton>
        </NForm>
      </div>

      <NDataTable :columns="columns" :data="roles" :row-key="(r: any) => r.id" :max-height="540" size="small" />
    </NDrawerContent>
  </NDrawer>

  <!-- 編輯角色 -->
  <NModal
    v-model:show="showEdit"
    preset="dialog"
    :title="`編輯角色：${editing?.name || ''}`"
    style="width: 420px"
  >
    <NForm :model="editForm" label-placement="left" label-width="56">
      <NFormItem label="名稱" required>
        <NInput v-model:value="editForm.name" />
      </NFormItem>
      <NFormItem label="描述">
        <NInput v-model:value="editForm.description" />
      </NFormItem>
    </NForm>
    <template #action>
      <NButton @click="showEdit = false">取消</NButton>
      <NButton type="primary" :loading="saving" @click="handleEdit">保存</NButton>
    </template>
  </NModal>
</template>
