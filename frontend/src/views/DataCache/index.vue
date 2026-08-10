<template>
  <div class="space-y-6">
    <div class="bg-[#131826] p-6 rounded-2xl border border-slate-800/80 space-y-2">
      <h3 class="text-base font-bold text-white flex items-center gap-2">
        <el-icon class="text-purple-400"><FolderOpened /></el-icon> 多级存储与异构本地盘缓存管理 (Data & Storage)
      </h3>
      <p class="text-xs text-slate-400">结合火山云 TOS 对象存储、NAS 共享存储以及节点本地 NVMe 高速缓存。</p>
    </div>

    <div class="grid grid-cols-3 gap-6">
      <el-card shadow="never" class="!bg-[#131826] !border-slate-800/80 !rounded-2xl">
        <template #header>
          <div class="flex justify-between items-center text-sm font-bold text-slate-200">
            <span>火山云 TOS 对象存储</span>
            <el-tag type="purple" size="small">S3 API</el-tag>
          </div>
        </template>
        <div class="space-y-3 text-xs font-mono text-slate-300">
          <div>Bucket 名称: <span class="text-purple-400">tos://ai-training-data</span></div>
          <div>Endpoint: <span class="text-slate-400">tos-cn-beijing.volces.com</span></div>
          <div>用途: 原始数据集、大模型 Checkpoint 归档</div>
        </div>
      </el-card>

      <el-card shadow="never" class="!bg-[#131826] !border-slate-800/80 !rounded-2xl">
        <template #header>
          <div class="flex justify-between items-center text-sm font-bold text-slate-200">
            <span>节点宿主机 NVMe 本地盘</span>
            <el-tag type="success" size="small">hostPath</el-tag>
          </div>
        </template>
        <div class="space-y-3 text-xs font-mono text-slate-300">
          <div>缓存路径: <span class="text-emerald-400">/mnt/local-disk/cache</span></div>
          <div>Spill 路径: <span class="text-emerald-400">/mnt/local-disk/ray_spill</span></div>
          <div>用途: Ray 对象池溢出与本地 Dataset 缓存</div>
        </div>
      </el-card>

      <el-card shadow="never" class="!bg-[#131826] !border-slate-800/80 !rounded-2xl">
        <template #header>
          <div class="flex justify-between items-center text-sm font-bold text-slate-200">
            <span>火山云 NAS 共享存储</span>
            <el-tag type="warning" size="small">PVC / NFS</el-tag>
          </div>
        </template>
        <div class="space-y-3 text-xs font-mono text-slate-300">
          <div>挂载卷: <span class="text-amber-400">pvc-shared-workspace</span></div>
          <div>挂载点: <span class="text-slate-400">/workspace</span></div>
          <div>用途: 多卡节点即时共享代码与环境</div>
        </div>
      </el-card>
    </div>
  </div>
</template>

<script setup>
</script>
