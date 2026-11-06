<script setup lang="ts">
// 評價管理抽屜：真實評價審核流（pending→通過/拒絕）+ 虛擬評價創建。
// catalog:review_read 查看 / catalog:review_manage 審核+虛擬評價（超管專屬）。
import { watch, ref, computed, h } from "vue";
import {
  NDrawer,
  NDrawerContent,
  NDataTable,
  NButton,
  NTag,
  NSpace,
  NModal,
  NForm,
  NFormItem,
  NInput,
  NInputNumber,
  NRate,
} from "naive-ui";
import type { DataTableColumns } from "naive-ui";
import { fetchReviews, approveReview, rejectReview, createVirtualReview } from "@/service/api";
import { checkAuth } from "@/directives";

defineOptions({ name: "ReviewsDrawer" });

const props = defineProps<{
  show: boolean;
  product: any;
}>();

const emit = defineEmits(["update:show"]);

const loading = ref(false);
const reviews = ref<any[]>([]);
const total = ref(0);
const page = ref(1);
const statusFilter = ref<string | null>(null);

const canManage = computed(() => checkAuth("catalog:review_manage"));

const statusOptions = [
  { label: "全部", value: "" },
  { label: "待審核", value: "pending" },
  { label: "已通過", value: "approved" },
  { label: "已拒絕", value: "rejected" },
];

function statusTag(s: string) {
  const type = s === "approved" ? "success" : s === "pending" ? "warning" : "error";
  const text = s === "approved" ? "已通過" : s === "pending" ? "待審核" : "已拒絕";
  return h(NTag, { type, size: "small" }, { default: () => text });
}

const columns: DataTableColumns<any> = [
  { title: "ID", key: "id", width: 60 },
  { title: "商品ID", key: "product_id", width: 76 },
  { title: "用户ID", key: "user_id", width: 76 },
  {
    title: "評分",
    key: "rating",
    width: 90,
    render: (row) => h(NRate, { value: row.rating, size: "small", readonly: true }),
  },
  { title: "內容", key: "content", minWidth: 180, ellipsis: true },
  { title: "狀態", key: "status", width: 84, render: (row) => statusTag(row.status) },
  {
    title: "時間",
    key: "created_at",
    width: 150,
    render: (row) => (row.created_at ? new Date(row.created_at * 1000).toLocaleString() : "-"),
  },
  {
    title: "操作",
    key: "actions",
    width: 130,
    render: (row) =>
      row.status === "pending" && canManage.value
        ? h(
            NSpace,
            { size: 4 },
            {
              default: () => [
                h(
                  NButton,
                  { size: "tiny", type: "success", secondary: true, onClick: () => handleApprove(row.id) },
                  { default: () => "通過" },
                ),
                h(
                  NButton,
                  { size: "tiny", type: "error", secondary: true, onClick: () => handleReject(row.id) },
                  { default: () => "拒絕" },
                ),
              ],
            },
          )
        : null,
  },
];

async function load() {
  loading.value = true;
  try {
    const { data, error } = await fetchReviews({
      status: statusFilter.value || undefined,
      page: page.value,
      page_size: 20,
    });
    if (!error && data) {
      reviews.value = (data as any).reviews || [];
      total.value = (data as any).total || 0;
    }
  } finally {
    loading.value = false;
  }
}

async function handleApprove(id: number) {
  const { error } = await approveReview(id);
  if (!error) {
    window.$message?.success("已通過（前台可見）");
    load();
  }
}

async function handleReject(id: number) {
  const { error } = await rejectReview(id);
  if (!error) {
    window.$message?.success("已拒絕（前台不可見）");
    load();
  }
}

// ── 虛擬評價 ──
const showVirtual = ref(false);
const virtualSaving = ref(false);
const virtualForm = ref({ nickname: "", content: "", rating: 5, sort: 0 });

function openVirtual() {
  if (props.product?.is_locked) return;
  virtualForm.value = { nickname: "", content: "", rating: 5, sort: 0 };
  showVirtual.value = true;
}

async function handleVirtual() {
  if (!props.product || props.product.is_locked || !virtualForm.value.content.trim()) return;
  virtualSaving.value = true;
  try {
    const { error } = await createVirtualReview({
      product_id: props.product.id,
      nickname: virtualForm.value.nickname.trim() || undefined,
      content: virtualForm.value.content.trim(),
      rating: virtualForm.value.rating,
      sort: virtualForm.value.sort,
    });
    if (!error) {
      window.$message?.success("虛擬評價已創建（與真實已通過評價合併展示）");
      showVirtual.value = false;
    }
  } finally {
    virtualSaving.value = false;
  }
}

watch(() => props.show, (show) => {
  if (show) void load();
}, { immediate: true });
</script>

<template>
  <NDrawer
    :show="show"
    :width="860"
    :auto-focus="false"
    @update:show="(v: boolean) => emit('update:show', v)"
  >
    <NDrawerContent :title="`評價管理${product ? '：' + product.name : ''}`" closable>
      <div class="mb-8px flex items-center gap-8px">
        <NSelect
          v-model:value="statusFilter"
          :options="statusOptions"
          class="w-120px"
          size="small"
          @update:value="
            page = 1;
            load();
          "
        />
        <span class="text-12px text-gray-400">共 {{ total }} 條真實評價</span>
        <NButton
          v-if="canManage && product"
          size="small"
          type="primary"
          class="ml-auto"
          :disabled="product.is_locked"
          :title="product.is_locked ? '請先解鎖商品後再添加虛擬評價' : ''"
          @click="openVirtual"
        >
          新增虛擬評價
        </NButton>
      </div>
      <NDataTable :columns="columns" :data="reviews" :loading="loading" size="small"  :max-height="540" />
      <div class="mt-8px flex items-center justify-between">
        <span class="text-12px text-gray-400">第 {{ page }} 頁</span>
        <div class="flex gap-8px">
          <NButton size="small" :disabled="page <= 1" @click="page--, load()">上一頁</NButton>
          <NButton size="small" :disabled="reviews.length < 20" @click="page++, load()">下一頁</NButton>
        </div>
      </div>
    </NDrawerContent>
  </NDrawer>

  <!-- 虛擬評價（catalog:review_manage 超管專屬） -->
  <NModal
    v-model:show="showVirtual"
    preset="dialog"
    :title="`虛擬評價：${product?.name || ''}`"
    style="width: 460px"
  >
    <NForm label-placement="top">
      <NFormItem label="暱稱">
        <NInput v-model:value="virtualForm.nickname" placeholder="選填，默認隨機暱稱" />
      </NFormItem>
      <NFormItem label="評分" required>
        <NRate v-model:value="virtualForm.rating" />
      </NFormItem>
      <NFormItem label="內容" required>
        <NInput
          v-model:value="virtualForm.content"
          type="textarea"
          :rows="3"
          placeholder="展示在前台商品評論區（與真實已通過評價合併）"
        />
      </NFormItem>
      <NFormItem label="排序">
        <NInputNumber v-model:value="virtualForm.sort" class="w-full" placeholder="小的靠前" />
      </NFormItem>
    </NForm>
    <template #action>
      <NButton @click="showVirtual = false">取消</NButton>
      <NButton type="primary" :loading="virtualSaving" @click="handleVirtual">創建</NButton>
    </template>
  </NModal>
</template>
