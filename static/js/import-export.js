/* ============================================================
   import-export.js - 配置导入与导出
   支持将收藏、标签、API配置、提示词等导出为 JSON 文件
   支持从 JSON 文件恢复所有配置
   ============================================================ */

const ImportExport = (() => {
    const t = (typeof I18n !== 'undefined' ? I18n.t : (s) => s);
    // DOM
    let btnImport, btnExport, hiddenImportInput;
    let bound = false;

    // ==================== 初始化（首次加载，DOM 固定时调用） ====================

    function init() {
        bindEvents();
    }

    // ==================== 绑定事件（可重复调用，用于动态 DOM 切换后重新绑定） ====================

    function bindEvents() {
        btnImport = document.getElementById('btnImport');
        btnExport = document.getElementById('btnExport');
        hiddenImportInput = document.getElementById('hiddenImportInput');

        if (btnExport && !bound) {
            btnExport.addEventListener('click', exportData);
            bound = true;
        }
        if (btnImport && !btnImport._listenerBound) {
            btnImport.addEventListener('click', () => hiddenImportInput && hiddenImportInput.click());
            btnImport._listenerBound = true;
        }
        if (hiddenImportInput && !hiddenImportInput._listenerBound) {
            hiddenImportInput.addEventListener('change', importData);
            hiddenImportInput._listenerBound = true;
        }
    }

    // ==================== 导出 ====================

    async function exportData() {
        try {
            App.showToast(t('toast.export_preparing'), 'info');

            const data = await Storage.exportAllData();
            const jsonStr = JSON.stringify(data, null, 2);

            const timestamp = new Date().toISOString().replace(/[:.]/g, '-').substring(0, 19);
            const filename = `LocalGallery_Backup_${timestamp}.json`;

            // Wails 环境：使用原生保存对话框
            if (typeof WailsBridge !== 'undefined' && WailsBridge.isWails()) {
                const savedPath = await WailsBridge.saveFile(filename, jsonStr);
                if (!savedPath) {
                    App.showToast(t('toast.export_cancelled'), 'info');
                    return;
                }
                const stats = buildStatsText(data);
                App.showToast(t('toast.exported_to').replace('{path}', savedPath) + stats, 'success');
                return;
            }

            // 浏览器环境：自动下载
            const blob = new Blob([jsonStr], { type: 'application/json' });
            const url = URL.createObjectURL(blob);
            const a = document.createElement('a');
            a.href = url;
            a.download = filename;
            document.body.appendChild(a);
            a.click();
            document.body.removeChild(a);
            URL.revokeObjectURL(url);

            const stats = buildStatsText(data);
            App.showToast(t('toast.exported').replace('{name}', filename) + stats, 'success');
        } catch (err) {
            if (err.message === '已取消') {
                App.showToast(t('toast.export_cancelled'), 'info');
                return;
            }
            console.error('[ImportExport] 导出失败:', err);
            App.showToast(t('toast.export_failed') + ': ' + err.message, 'error');
        }
    }

    function buildStatsText(data) {
        const stats = [];
        if (data.tags && data.tags.length > 0) stats.push(data.tags.length + t('import.stats_tag'));
        if (data.favorites && data.favorites.length > 0) stats.push(data.favorites.length + t('import.stats_favorite'));
        if (data.apiConfigs && data.apiConfigs.length > 0) stats.push(data.apiConfigs.length + t('import.stats_api'));
        if (data.promptVersions && data.promptVersions.length > 0) stats.push(data.promptVersions.length + t('import.stats_prompt'));
        if (data.imageTags && data.imageTags.length > 0) stats.push(data.imageTags.length + t('import.stats_image_tag'));
        return stats.length > 0 ? ` (${stats.join(', ')})` : '';
    }

    // ==================== 导入 ====================

    async function importData(event) {
        const file = event.target.files[0];
        if (!file) return;

        try {
            // 显示导入选项对话框
            const mergeMode = await showImportDialog(file.name);

            if (mergeMode === null) {
                // 用户取消
                hiddenImportInput.value = '';
                return;
            }

            App.showToast(t('toast.import_preparing'), 'info');

            const text = await file.text();
            const data = JSON.parse(text);

            // 验证数据格式
            if (!data.version) {
                throw new Error('无效的备份文件格式（缺少版本号）');
            }

            if (mergeMode === 'replace') {
                // 替换模式：清除现有数据
                await clearAllData();
            }

            await Storage.importAllData(data);

            // 统计导入数据
            const stats = [];
            if (data.tags) stats.push(data.tags.length + t('import.stats_tag'));
            if (data.favorites) stats.push(data.favorites.length + t('import.stats_favorite'));
            if (data.apiConfigs) stats.push(data.apiConfigs.length + t('import.stats_api'));
            if (data.promptVersions) stats.push(data.promptVersions.length + t('import.stats_prompt'));

            const statsText = stats.length > 0 ? ` (${stats.join(', ')})` : '';
            const modeText = mergeMode === 'replace' ? t('import.mode_replace') : t('import.mode_merge');
            App.showToast(t('toast.import_complete').replace('{info}', modeText + statsText), 'success');

            // 刷新 UI
            await Sidebar.refreshTagTree();
            await Sidebar.refreshApiConfigSelect();
            Gallery.render();

            // 如果右侧面板有图片，刷新其标签
            const currentImg = DetailPanel.getCurrentImage();
            if (currentImg) {
                DetailPanel.showImage(currentImg);
            }
        } catch (err) {
            console.error('[ImportExport] 导入失败:', err);
            App.showToast(t('toast.import_failed_short') + ': ' + err.message, 'error');
        } finally {
            hiddenImportInput.value = '';
        }
    }

    /**
     * 显示导入选项对话框
     * @returns {Promise<string|null>} 'merge', 'replace', 或 null（取消）
     */
    function showImportDialog(filename) {
        return new Promise((resolve) => {
            const overlay = document.getElementById('modalOverlay');
            const content = document.getElementById('modalContent');

            content.innerHTML = `
                <h2><span class="icon icon-import"></span> 导入配置</h2>
                <p style="color: var(--text-secondary); margin-bottom: 16px;">
                    即将从文件 <strong style="color: var(--accent);">${escapeHtml(filename)}</strong> 导入数据
                </p>
                <div style="margin-bottom: 16px;">
                    <label style="display: flex; align-items: flex-start; gap: 10px; padding: 12px; background: var(--bg-input); border-radius: var(--radius); margin-bottom: 8px; cursor: pointer;">
                        <input type="radio" name="importMode" value="merge" checked style="margin-top: 2px;" />
                        <div>
                            <strong><span class="icon icon-refresh"></span> 合并模式</strong>
                            <p style="font-size: 12px; color: var(--text-muted); margin-top: 4px;">保留现有数据，仅添加新数据（重复项自动跳过）</p>
                        </div>
                    </label>
                    <label style="display: flex; align-items: flex-start; gap: 10px; padding: 12px; background: var(--bg-input); border-radius: var(--radius); cursor: pointer;">
                        <input type="radio" name="importMode" value="replace" style="margin-top: 2px;" />
                        <div>
                            <strong><span class="icon icon-warning"></span> 替换模式</strong>
                            <p style="font-size: 12px; color: var(--text-muted); margin-top: 4px;">清除所有现有数据，完全替换为导入的数据</p>
                        </div>
                    </label>
                </div>
                <div class="modal-actions">
                    <button id="btnCancelImport" class="btn-secondary">取消</button>
                    <button id="btnConfirmImport" class="btn-primary">确认导入</button>
                </div>
            `;

            overlay.style.display = 'flex';

            document.getElementById('btnCancelImport').addEventListener('click', () => {
                overlay.style.display = 'none';
                resolve(null);
            });

            document.getElementById('btnConfirmImport').addEventListener('click', () => {
                overlay.style.display = 'none';
                const selected = document.querySelector('input[name="importMode"]:checked');
                resolve(selected ? selected.value : 'merge');
            });

            // 点击遮罩关闭
            overlay.addEventListener('click', (e) => {
                if (e.target === overlay) {
                    overlay.style.display = 'none';
                    resolve(null);
                }
            });
        });
    }

    /**
     * 清除所有 IndexedDB 数据
     */
    async function clearAllData() {
        const dbName = 'LocalGalleryDB';
        return new Promise((resolve, reject) => {
            const request = indexedDB.deleteDatabase(dbName);
            request.onsuccess = () => {
                console.log('[ImportExport] 旧数据库已清除');
                // 重新初始化
                Storage.init().then(resolve).catch(reject);
            };
            request.onerror = () => reject(request.error);
            request.onblocked = () => {
                console.warn('[ImportExport] 数据库删除被阻止，请关闭其他标签页');
                reject(new Error('数据库被其他标签页占用，请关闭后重试'));
            };
        });
    }

    // ==================== 工具函数 ====================

    function escapeHtml(str) {
        const div = document.createElement('div');
        div.textContent = str;
        return div.innerHTML;
    }

    // ==================== 公开 API ====================

    return {
        init,
        bindEvents,
        exportData,
        importData
    };
})();
