/* ============================================================
   settings.js - 设置弹出页
   ============================================================ */

const Settings = (() => {
    const t = (typeof I18n !== "undefined" ? I18n.t : (s) => s);
    let btnThumbSettings;

    function init() {
        btnThumbSettings = document.getElementById('btnThumbSettings');
        if (btnThumbSettings) {
            btnThumbSettings.addEventListener('click', openSettingsDialog);
        }
    }

    // ==================== 打开设置对话框 ====================

    let _settingsChanged = false;

    async function openSettingsDialog() {
        const t = (typeof I18n !== "undefined" ? I18n.t : (s) => s);
        _settingsChanged = false;
        const overlay = document.getElementById('modalOverlay');
        const content = document.getElementById('modalContent');

        content.innerHTML = `
            <div class="settings-dialog">
                <h2><span class="icon icon-settings"></span> ${t("settings.title")}</h2>

                <!-- 1. 配色方案 -->
                <div class="settings-section">
                    <h4>${t("settings.color_scheme")}</h4>
                    <div class="color-presets" id="colorPresets">
                        <button class="color-swatch active" data-color="#e94560" style="background:#e94560" title="${t('settings.color.rose')}"></button>
                        <button class="color-swatch" data-color="#4a9eff" style="background:#4a9eff" title="${t('settings.color.sky')}"></button>
                        <button class="color-swatch" data-color="#4caf50" style="background:#4caf50" title="${t('settings.color.green')}"></button>
                        <button class="color-swatch" data-color="#7c3aed" style="background:#7c3aed" title="${t('settings.color.violet')}"></button>
                        <button class="color-swatch" data-color="#f59e0b" style="background:#f59e0b" title="${t('settings.color.amber')}"></button>
                        <button class="color-swatch" data-color="#14b8a6" style="background:#14b8a6" title="${t('settings.color.teal')}"></button>
                        <button class="color-swatch" data-color="#ec4899" style="background:#ec4899" title="${t('settings.color.pink')}"></button>
                        <button class="color-swatch" data-color="#6366f1" style="background:#6366f1" title="${t('settings.color.indigo')}"></button>
                    </div>
                    <div class="color-custom-row">
                        <label for="settingsAccentColor">${t("settings.color.custom")}:</label>
                        <input type="color" id="settingsAccentColor" class="settings-color-picker" value="#e94560" />
                        <span class="color-hex" id="colorHexLabel">#e94560</span>
                    </div>
                </div>

                <!-- 3. 目录路径设置 -->
                <div class="settings-section">
                    <h4>${t("settings.dir_paths")}</h4>
                    <div style="margin-bottom: 14px;">
                        <span style="font-size: 12px; color: var(--text-secondary); font-weight: 600;">${t("settings.thumb_cache_dir")}</span>
                        <div class="settings-dir-row" style="margin-top: 4px;">
                            <input type="text" id="settingsThumbDir" class="settings-dir-input" readonly />
                            <button id="settingsBrowseThumbDir" class="btn-small" title="${t("settings.select_thumb_dir")}"><span class="icon icon-browse"></span></button>
                            <button id="settingsResetThumbDir" class="btn-small" title="${t("settings.reset_default")}"><span class="icon icon-reset"></span></button>
                        </div>
                        <span class="input-hint" id="settingsThumbDirHint"></span>
                    </div>
                    <div>
                        <span style="font-size: 12px; color: var(--text-secondary); font-weight: 600;">${t("settings.user_data_dir")}</span>
                        <div class="settings-dir-row" style="margin-top: 4px;">
                            <input type="text" id="settingsUserDataDir" class="settings-dir-input" readonly />
                            <button id="settingsBrowseUserDataDir" class="btn-small" title="${t("settings.select_user_dir")}"><span class="icon icon-browse"></span></button>
                            <button id="settingsResetUserDataDir" class="btn-small" title="${t("settings.reset_default_dir")}"><span class="icon icon-reset"></span></button>
                        </div>
                        <span class="input-hint" id="settingsUserDataDirHint" style="color: var(--text-muted);">${t("settings.user_data_hint")}</span>
                    </div>
                </div>

                <!-- 4. 导入导出 -->
                <div class="settings-section">
                    <h4>${t("settings.data_backup")}</h4>
                    <div style="display: flex; gap: 8px;">
                        <button id="btnImport" class="btn-small" title="${t("settings.import_config")}"><span class="icon icon-import"></span> ${t("settings.import_config")}</button>
                        <button id="btnExport" class="btn-small" title="${t("settings.export_config")}"><span class="icon icon-export"></span> ${t("settings.export_config")}</button>
                        <input type="file" id="hiddenImportInput" accept=".json" style="display: none;" />
                    </div>
                    <span class="input-hint">${t("settings.import_export_hint")}</span>
                </div>

                <!-- 5. 缓存统计 / 缩略图生成 / 完成预览图（合并卡片） -->
                <div class="settings-section" style="padding-bottom: 10px;">
                    <!-- 5a. 缓存统计 -->
                    <h4>${t("settings.cache_stats")}</h4>
                    <div class="settings-stats" style="margin-bottom: 6px;">
                        <span class="settings-stat">${t("settings.thumb_count")}: <strong id="statCount">--</strong></span>
                        <span class="settings-stat">${t("settings.space_used")}: <strong id="statSize">--</strong></span>
                    </div>
                    <div class="settings-stats-actions" style="margin-bottom: 8px;">
                        <button id="settingsRefreshStats" class="btn-small"><span class="icon icon-refresh"></span> ${t("settings.refresh")}</button>
                        <button id="settingsCleanOrphaned" class="btn-small btn-danger"><span class="icon icon-clean"></span> ${t("settings.clean_orphan")}</button>
                    </div>
                    <div style="font-size: 11px; color: var(--text-muted); margin-bottom: 4px;">${t("settings.folder_ops_hint")}</div>
                    <div style="margin-bottom: 6px; display: flex; justify-content: space-between; align-items: center;">
                        <button id="settingsSelectAllFolders" class="btn-small">${t("settings.select_all")}</button>
                        <button id="settingsClearFolderThumbs" class="btn-small btn-danger" disabled><span class="icon icon-delete"></span> ${t("settings.clear_selection")}</button>
                    </div>
                    <div id="settingsFolderTree" class="thumb-folder-tree" style="min-height: 40px; margin-bottom: 6px;">
                        <span style="font-size: 11px; color: var(--text-muted); padding: 4px;">${t("gallery.loading")}</span>
                    </div>

                    <hr style="border: none; border-top: 1px solid var(--border-light); margin: 0 0 12px 0;" />

                    <!-- 5b. 完成预览图 -->
                    <h4>${t("settings.pregen")}</h4>
                    <div style="font-size: 11px; color: var(--text-muted); margin-bottom: 8px;">${t("settings.pregen_hint")}</div>
                    <div id="settingsPreGenStatus" style="margin-bottom: 8px; display: none;">
                        <div style="height: 6px; background: var(--bg-tertiary); border-radius: 3px; overflow: hidden; margin-bottom: 4px;">
                            <div id="settingsPreGenProgress" style="height: 100%; width: 0%; background: var(--accent); border-radius: 3px; transition: width 0.3s;"></div>
                        </div>
                        <span id="settingsPreGenText" style="font-size: 11px; color: var(--text-secondary);"></span>
                    </div>
                    <div>
                        <button id="settingsStartPreGen" class="btn-small" disabled>${t("settings.start")}</button>
                        <button id="settingsPausePreGen" class="btn-small" style="display: none;">${t("settings.pause")}</button>
                        <button id="settingsResumePreGen" class="btn-small" style="display: none;">${t("settings.continue")}</button>
                        <button id="settingsStopPreGen" class="btn-small btn-danger" style="display: none;"><span class="icon icon-stop"></span> ${t("settings.stop")}</button>
                    </div>

                    <hr style="border: none; border-top: 1px solid var(--border-light); margin: 8px 0 12px 0;" />

                    <!-- 5c. 缩略图生成 -->
                    <h4>${t("settings.thumb_gen")}</h4>
                    <div class="settings-row" style="margin-bottom: 6px;">
                        <label for="settingsThumbConcurrency">${t("settings.concurrency")}:</label>
                        <input type="number" id="settingsThumbConcurrency" class="settings-number" min="1" max="64" value="2" />
                        <button id="settingsApplyConcurrency" class="btn-small">${t("settings.apply")}</button>
                        <span class="input-hint" style="margin-left: 8px;">(1-64)</span>
                    </div>
                    <!-- 缩放算法暂时隐藏
                    <div class="settings-row" style="margin-bottom: 8px;">
                        <label for="settingsThumbKernel">${t("settings.scale_algo")}:</label>
                        <select id="settingsThumbKernel" class="settings-select">
                            <option value="lanczos3">Lanczos3 (${t("settings.lanczos3")})</option>
                            <option value="lanczos2">Lanczos2 (${t("settings.lanczos2")})</option>
                            <option value="cubic">Cubic</option>
                            <option value="mitchell">Mitchell</option>
                            <option value="linear">Linear</option>
                            <option value="nearest">Nearest</option>
                        </select>
                        <button id="settingsApplyKernel" class="btn-small">${t("settings.apply")}</button>
                    </div>
                    -->
                </div>

                <!-- 6. 手机/平板访问 (WiFi) -->
                <div class="settings-section">
                    <h4>${t("settings.wifi_access") || "WiFi 访问"}</h4>
                    <p style="font-size: 11px; color: var(--text-muted); margin: 0 0 8px 0;">${t("settings.wifi_access_hint") || "开启后，同一 WiFi 下的手机、平板可通过浏览器访问"}</p>
                    <div style="margin-bottom: 6px; display: flex; align-items: center; gap: 8px;">
                        <label for="settingsLanIP" style="font-size: 12px; color: var(--text-secondary); font-weight: 600; min-width: 56px;">${t("settings.wifi_ip") || "本机地址"}:</label>
                        <input type="text" id="settingsLanIP" class="settings-dir-input" style="flex: 1;" readonly placeholder="自动检测" />
                    </div>
                    <div style="margin-bottom: 6px; display: flex; align-items: center; gap: 8px;">
                        <label for="settingsLanPort" style="font-size: 12px; color: var(--text-secondary); font-weight: 600; min-width: 56px;">${t("settings.wifi_port") || "端口号"}:</label>
                        <input type="number" id="settingsLanPort" class="settings-dir-input" min="1" max="65535" value="25876" style="width: 100px; text-align: center; flex: none;" />
                    </div>
                    <div style="margin-bottom: 8px; display: flex; align-items: center; gap: 8px;">
                        <label style="font-size: 12px; color: var(--text-secondary); font-weight: 600;">
                            <input type="checkbox" id="settingsLanAutoStart" style="margin-right: 4px;" />
                            ${t("settings.wifi_access_auto") || "开机自动启动"}
                        </label>
                    </div>
                    <div style="display: flex; gap: 8px; align-items: center;">
                        <button id="settingsStartLAN" class="btn-small"><span class="icon icon-play"></span> ${t("settings.wifi_start") || "开启访问"}</button>
                        <button id="settingsStopLAN" class="btn-small btn-danger" style="display: none;"><span class="icon icon-stop"></span> ${t("settings.wifi_stop") || "关闭访问"}</button>
                        <span id="settingsLanStatus" style="font-size: 11px; color: var(--text-muted);"></span>
                    </div>
                    <div id="settingsLanURL" style="margin-top: 6px; font-size: 11px; display: none;">
                        <span style="color: var(--accent);" id="settingsLanURLText"></span>
                    </div>
                </div>
            </div>
            <div class="modal-actions">
                <button id="btnCloseSettings" class="btn-primary">${t("settings.close")}</button>
            </div>
        `;

        overlay.style.display = 'flex';

        document.getElementById('btnCloseSettings').addEventListener('click', () => {
            stopPreGenPolling();
            overlay.style.display = 'none';
            if (_settingsChanged) {
                window.runtime.Quit();
            }
        });

        // ===== 配色方案事件 =====
        initColorPickers();

        document.getElementById('settingsBrowseThumbDir').addEventListener('click', browseThumbDir);
        document.getElementById('settingsResetThumbDir').addEventListener('click', resetThumbDir);
        document.getElementById('settingsBrowseUserDataDir').addEventListener('click', browseUserDataDir);
        document.getElementById('settingsResetUserDataDir').addEventListener('click', resetUserDataDir);
        document.getElementById('settingsRefreshStats').addEventListener('click', loadCacheStats);
        document.getElementById('settingsCleanOrphaned').addEventListener('click', cleanOrphanedThumbs);
        document.getElementById('settingsApplyConcurrency').addEventListener('click', applyConcurrency);
        // document.getElementById('settingsApplyKernel').addEventListener('click', applyKernel); // 缩放算法暂时隐藏
        document.getElementById('settingsClearFolderThumbs').addEventListener('click', clearSelectedFolderThumbs);
        document.getElementById('settingsStartLAN').addEventListener('click', startLANServer);
        document.getElementById('settingsStopLAN').addEventListener('click', stopLANServer);
        // 复选框即时保存到 localStorage
        document.getElementById('settingsLanAutoStart').addEventListener('change', function() {
            localStorage.setItem('lanAutoStart', String(this.checked));
        });

        // ===== 全选按钮 =====
        document.getElementById('settingsSelectAllFolders').addEventListener('click', function() {
            var allCbs = document.querySelectorAll('#settingsFolderTree .thumb-folder-tree-check');
            if (allCbs.length === 0) return;
            var allChecked = true;
            allCbs.forEach(function(cb) { if (!cb.checked) allChecked = false; });
            var newState = !allChecked;
            allCbs.forEach(function(cb) {
                cb.checked = newState;
                var row = cb.closest('.thumb-folder-tree-row');
                var path = row ? row.dataset.path : '';
                if (newState) {
                    _selectedFolderPaths.add(path);
                    if (row) row.classList.add('selected');
                } else {
                    _selectedFolderPaths.delete(path);
                    if (row) row.classList.remove('selected');
                }
            });
            updateClearBtn();
        });

        // ===== 预生成缩略图事件 =====
        var btnStartPreGen = document.getElementById('settingsStartPreGen');
        var btnPausePreGen = document.getElementById('settingsPausePreGen');
        var btnResumePreGen = document.getElementById('settingsResumePreGen');
        var btnStopPreGen = document.getElementById('settingsStopPreGen');

        btnStartPreGen.addEventListener('click', function() {
            if (_selectedFolderPaths.size === 0) return;
            var folderArr = Array.from(_selectedFolderPaths);
            WailsBridge.startPreGenThumbs(folderArr).then(function(r) {
                if (r && r.success) {
                    btnStartPreGen.style.display = 'none';
                    btnPausePreGen.style.display = '';
                    btnStopPreGen.style.display = '';
                    document.getElementById('settingsPreGenStatus').style.display = '';
                    document.getElementById('settingsPreGenText').textContent = t('settings.preparing');
                    document.getElementById('settingsPreGenProgress').style.width = '0%';
                    startPreGenPolling();
                } else {
                    App.showToast((r && r.message) || t('settings.start_failed'), 'warning');
                }
            }).catch(function(e) {
                App.showToast(t('settings.start_failed') + ': ' + (e.message || String(e)), 'error');
            });
        });

        btnPausePreGen.addEventListener('click', function() {
            WailsBridge.pausePreGenThumbs();
            btnPausePreGen.style.display = 'none';
            btnResumePreGen.style.display = '';
        });

        btnResumePreGen.addEventListener('click', function() {
            WailsBridge.resumePreGenThumbs();
            btnResumePreGen.style.display = 'none';
            btnPausePreGen.style.display = '';
        });

        btnStopPreGen.addEventListener('click', function() {
            WailsBridge.stopPreGenThumbs();
            stopPreGenPolling();
            resetPreGenButtons();
        });

        // 打开设置时检查是否已有预生成任务在运行
        checkPreGenStatus();
        startPreGenPollingIfNeeded();
        if (typeof ImportExport !== 'undefined') ImportExport.bindEvents();

        overlay.addEventListener('click', (e) => {
            if (e.target === overlay) {
                stopPreGenPolling();
                overlay.style.display = 'none';
                if (_settingsChanged) {
                    window.runtime.Quit();
                }
            }
        });

        loadCurrentDirs();
        loadCacheStats();
        loadThumbGenSettings();
        loadLANInfo();
        populateFolderDropdown();
    }

    // ==================== 加载当前目录 ====================

    async function loadCurrentDirs() {
        const tfn = (typeof I18n !== "undefined" ? I18n.t : (s) => s);
        const thumbInput = document.getElementById('settingsThumbDir');
        const thumbHint = document.getElementById('settingsThumbDirHint');
        const userDataInput = document.getElementById('settingsUserDataDir');

        if (typeof WailsBridge !== 'undefined' && WailsBridge.isWails()) {
            try {
                const thumbDir = await WailsBridge.getThumbDir();
                if (thumbInput) {
                    thumbInput.value = thumbDir;
                    thumbHint.innerHTML = '<span class="icon icon-check"></span> ' + tfn('settings.thumb_cache_dir');
                    thumbHint.style.color = 'var(--green)';
                }
            } catch (e) {
                console.error('[loadCurrentDirs] getThumbDir 失败:', e);
                if (thumbHint) thumbHint.innerHTML = '<span class="icon icon-warning"></span> ' + tfn('settings.load_failed');
            }

            try {
                const userDataDir = await WailsBridge.getUserDataDir();
                console.log('[loadCurrentDirs] getUserDataDir 返回:', userDataDir, 'input:', !!userDataInput);
                if (userDataInput) userDataInput.value = userDataDir;
            } catch (e) {
                console.error('[loadCurrentDirs] getUserDataDir 失败:', e);
                if (userDataInput) userDataInput.value = tfn('settings.load_failed');
            }
        }
    }

    // ==================== 更新前端缓存 ====================

    async function syncSettingsCache(key, value) {
        if (typeof Storage !== 'undefined' && Storage.setSetting) {
            await Storage.setSetting(key, value);
        }
        if (typeof Storage !== 'undefined' && Storage.saveToServer) {
            await Storage.saveToServer();
        }
    }

    // ==================== 加载缓存统计 ====================

    async function loadCacheStats() {
        const countEl = document.getElementById('statCount');
        const sizeEl = document.getElementById('statSize');

        if (typeof WailsBridge !== 'undefined' && WailsBridge.isWails()) {
            try {
                const info = await WailsBridge.getThumbCacheInfo();
                if (countEl) countEl.textContent = info.count;
                if (sizeEl) sizeEl.textContent = info.totalSizeStr;
            } catch (e) {
                if (countEl) countEl.textContent = '--';
                if (sizeEl) sizeEl.textContent = '--';
            }
        }
    }

    // ==================== 浏览/重置 缩略图目录 ====================

    async function browseThumbDir() {
        try {
            const folderPath = await WailsBridge.selectFolder();
            if (!folderPath) return;

            await syncSettingsCache('thumbDir', folderPath);

            const result = await WailsBridge.setThumbDir(folderPath);
            if (result && result.success) {
                _settingsChanged = true;
                document.getElementById('settingsThumbDir').value = result.thumbDir;
                const hint = document.getElementById('settingsThumbDirHint');
                hint.innerHTML = '<span class="icon icon-check"></span> ' + t('settings.cache_dir_updated');
                hint.style.color = 'var(--green)';
                App.showToast(t('settings.cache_dir_updated'), 'success');
                loadCacheStats();
            } else {
                await syncSettingsCache('thumbDir', await WailsBridge.getThumbDir());
                App.showToast(t('settings.save_failed') + ': ' + (result.error || t('settings.unknown_error')), 'error');
            }
        } catch (e) {
            App.showToast(t('settings.select_dir_failed') + ': ' + e.message, 'error');
        }
    }

    async function resetThumbDir() {
        try {
            const result = await WailsBridge.setThumbDir('');
            if (result && result.success) {
                _settingsChanged = true;
                await syncSettingsCache('thumbDir', result.thumbDir);

                document.getElementById('settingsThumbDir').value = result.thumbDir;
                const hint = document.getElementById('settingsThumbDirHint');
                hint.innerHTML = '<span class="icon icon-check"></span> ' + t('settings.cache_dir_reset');
                hint.style.color = 'var(--green)';
                App.showToast(t('settings.cache_dir_reset'), 'success');
                loadCacheStats();
            } else {
                App.showToast(t('settings.reset_failed') + ': ' + (result.error || t('settings.unknown_error')), 'error');
            }
        } catch (e) {
            App.showToast(t('settings.reset_failed') + ': ' + e.message, 'error');
        }
    }

    // ==================== 清理孤立缩略图 ====================

    async function cleanOrphanedThumbs() {
        const btn = document.getElementById('settingsCleanOrphaned');
        btn.disabled = true;
        btn.innerHTML = '<span class="icon icon-loading"></span> ' + t('settings.cleaning');
        try {
            const result = await WailsBridge.cleanOrphanedThumbs();
            if (result && result.success) {
                App.showToast(t("settings.orphaned_cleaned", { n: result.cleaned }), 'success');
                if (typeof Gallery !== 'undefined' && Gallery.refreshThumbGen) {
                    await Gallery.refreshThumbGen();
                }
            } else {
                App.showToast(t('settings.clean_failed') + ': ' + (result.error || t('settings.unknown_error')), 'error');
            }
            loadCacheStats();
        } catch (e) {
            App.showToast(t('settings.clean_failed') + ': ' + e.message, 'error');
        } finally {
            btn.disabled = false;
            btn.innerHTML = '<span class="icon icon-clean"></span> ' + t('settings.clean_orphan');
        }
    }

    // ==================== 浏览/重置 用户数据目录 ====================

    async function browseUserDataDir() {
        try {
            const folderPath = await WailsBridge.selectFolder();
            if (!folderPath) return;

            const result = await WailsBridge.setUserDataDir(folderPath);
            if (result && result.success) {
                _settingsChanged = true;
                document.getElementById('settingsUserDataDir').value = result.userDataDir;
                const hint = document.getElementById('settingsUserDataDirHint');
                hint.innerHTML = '<span class="icon icon-check"></span> ' + t('settings.user_dir_set_hint');
                hint.style.color = 'var(--green)';
                App.showToast(t('settings.user_dir_set'), 'success');
            } else {
                App.showToast(t('settings.save_failed') + ': ' + (result.error || t('settings.unknown_error')), 'error');
            }
        } catch (e) {
            App.showToast(t('settings.select_dir_failed') + ': ' + e.message, 'error');
        }
    }

    async function resetUserDataDir() {
        try {
            const result = await WailsBridge.setUserDataDir('');
            if (result && result.success) {
                _settingsChanged = true;
                document.getElementById('settingsUserDataDir').value = result.userDataDir;
                const hint = document.getElementById('settingsUserDataDirHint');
                hint.innerHTML = '<span class="icon icon-check"></span> ' + t('settings.user_dir_reset_hint');
                hint.style.color = 'var(--green)';
                App.showToast(t('settings.user_dir_reset'), 'success');
            } else {
                App.showToast(t('settings.reset_failed') + ': ' + (result.error || t('settings.unknown_error')), 'error');
            }
        } catch (e) {
            App.showToast(t('settings.reset_failed') + ': ' + e.message, 'error');
        }
    }

    async function refreshAfterDataDirChange() {
        if (typeof Storage !== 'undefined' && Storage.syncFromServer) {
            await Storage.syncFromServer();
        }
        if (typeof Sidebar !== 'undefined') {
            await Sidebar.refreshFolderTree();
            await Sidebar.refreshTagTree();
            await Sidebar.refreshApiConfigSelect();
        }
        await loadCurrentDirs();
        await loadCacheStats();
    }

    // ==================== 缩略图生成设置 ====================

    async function loadThumbGenSettings() {
        if (typeof WailsBridge === 'undefined' || !WailsBridge.isWails()) return;

        try {
            const concurrency = await WailsBridge.getThumbConcurrency();
            const concurrencyInput = document.getElementById('settingsThumbConcurrency');
            if (concurrencyInput) concurrencyInput.value = concurrency;
        } catch (e) {
            console.warn('[设置] 加载并发数失败:', e);
        }

        // 缩放算法暂时隐藏
        /*
        try {
            const kernel = await WailsBridge.getThumbKernel();
            const kernelSelect = document.getElementById('settingsThumbKernel');
            if (kernelSelect) kernelSelect.value = kernel;
        } catch (e) {
            console.warn('[设置] 加载缩放算法失败:', e);
        }
        */
    }

    async function applyConcurrency() {
        const input = document.getElementById('settingsThumbConcurrency');
        const n = parseInt(input.value, 10);
        if (isNaN(n) || n < 1 || n > 64) {
            App.showToast(t('settings.concurrency_range'), 'error');
            return;
        }

        try {
            const result = await WailsBridge.setThumbConcurrency(n);
            if (result && result.success) {
                App.showToast(t("settings.concurrency_updated") + " " + result.thumbConcurrency, 'success');
            } else {
                App.showToast(t('settings.save_failed') + ': ' + (result.error || t('settings.unknown_error')), 'error');
            }
        } catch (e) {
            App.showToast(t('settings.save_failed') + ': ' + e.message, 'error');
        }
    }

    // 缩放算法暂时隐藏
    /*
    async function applyKernel() {
        const select = document.getElementById('settingsThumbKernel');
        const kernel = select.value;

        try {
            const result = await WailsBridge.setThumbKernel(kernel);
            if (result && result.success) {
                App.showToast(t("settings.kernel_updated") + " " + result.thumbKernel, 'success');
            } else {
                App.showToast(t('settings.save_failed') + ': ' + (result.error || t('settings.unknown_error')), 'error');
            }
        } catch (e) {
            App.showToast(t('settings.save_failed') + ': ' + e.message, 'error');
        }
    }
    */

    // ==================== 按文件夹清除缩略图 ====================

    let _selectedFolderPaths = new Set();
    let _preGenPollTimer = null;

    async function populateFolderDropdown() {
        const tree = document.getElementById('settingsFolderTree');
        if (!tree) return;

        try {
            const folders = await WailsBridge.getFolders();
            if (!folders || !Array.isArray(folders) || folders.length === 0) {
                tree.innerHTML = '<span style="font-size: 11px; color: var(--text-muted); padding: 4px;">' + t('settings.no_folders') + '</span>';
                return;
            }

            tree.innerHTML = '';
            _selectedFolderPaths.clear();
            updateClearBtn();

            function buildNodes(nodes, depth) {
                const frag = document.createDocumentFragment();
                for (const node of nodes) {
                    const hasKids = node.children && node.children.length > 0;
                    const count = node.imageCount || 0;

                    const row = document.createElement('div');
                    row.className = 'thumb-folder-tree-row';
                    row.style.paddingLeft = (4 + depth * 14) + 'px';
                    row.dataset.path = node.path;
                    row.title = node.path;

                    const cb = document.createElement('input');
                    cb.type = 'checkbox';
                    cb.className = 'thumb-folder-tree-check';
                    cb.addEventListener('click', function(e) {
                        e.stopPropagation();
                        if (cb.checked) {
                            _selectedFolderPaths.add(node.path);
                            row.classList.add('selected');
                        } else {
                            _selectedFolderPaths.delete(node.path);
                            row.classList.remove('selected');
                        }
                        updateClearBtn();
                    });
                    row.appendChild(cb);

                    const toggle = document.createElement('span');
                    toggle.className = 'thumb-folder-tree-toggle ' + (hasKids ? 'collapsed' : 'leaf');
                    row.appendChild(toggle);

                    const name = document.createElement('span');
                    name.className = 'thumb-folder-tree-name';
                    name.textContent = node.name;
                    row.appendChild(name);

                    // 刷新按钮：重新扫描该文件夹
                    const refreshBtn = document.createElement('button');
                    refreshBtn.className = 'thumb-folder-tree-refresh';
                    refreshBtn.innerHTML = '<span class="icon icon-refresh"></span>';
                    refreshBtn.title = t('settings.rescan_folder');
                    let isRefreshing = false;
                    refreshBtn.addEventListener('click', async (e) => {
                        e.stopPropagation();
                        e.preventDefault();
                        if (isRefreshing) return;
                        isRefreshing = true;
                        refreshBtn.disabled = true;
                        refreshBtn.innerHTML = '<span class="icon icon-loading"></span>';
                        try {
                            const normalizedPath = (node.path || '').replace(/\\/g, '/');
                            let rootPath = node.path;
                            if (typeof Gallery !== 'undefined' && Gallery.getImportedRoots) {
                                const roots = Gallery.getImportedRoots();
                                for (const r of roots) {
                                    const rId = (r.rootId || '').replace(/\\/g, '/');
                                    if (normalizedPath === rId || normalizedPath.startsWith(rId + '/')) {
                                        rootPath = r.rootId || r.path;
                                        break;
                                    }
                                }
                            }
                            if (typeof WailsBridge !== 'undefined') {
                                const result = await WailsBridge.fullRescanFolder(rootPath);
                                if (result && result.success) {
                                    App.showToast(result.message || t('settings.folder_full_rescan_started'), 'success');
                                } else {
                                    App.showToast((result && result.message) || t('settings.refresh_failed'), 'error');
                                }
                            }
                            if (typeof Gallery !== 'undefined' && Gallery.refreshRootFromServer) {
                                await Gallery.refreshRootFromServer(rootPath);
                            }
                            if (typeof Sidebar !== 'undefined' && Sidebar.refreshFolderTree) {
                                await Sidebar.refreshFolderTree();
                            }
                            populateFolderDropdown();
                        } catch (err) {
                            console.error('[Settings] 刷新文件夹失败:', err);
                            App.showToast(t('settings.refresh_failed') + ': ' + err.message, 'error');
                        } finally {
                            isRefreshing = false;
                            refreshBtn.disabled = false;
                            refreshBtn.innerHTML = '<span class="icon icon-refresh"></span>';
                        }
                    });

                    if (count > 0) {
                        const cnt = document.createElement('span');
                        cnt.className = 'thumb-folder-tree-count';
                        cnt.textContent = count;
                        row.appendChild(cnt);
                    }

                    // 刷新按钮放在数字右侧
                    row.appendChild(refreshBtn);

                    row.addEventListener('click', (e) => {
                        e.stopPropagation();
                        if (e.target === toggle) {
                            toggleFolder(tree, row, hasKids);
                            return;
                        }
                        // 点击行 = 切换复选框
                        cb.checked = !cb.checked;
                        if (cb.checked) {
                            _selectedFolderPaths.add(node.path);
                            row.classList.add('selected');
                        } else {
                            _selectedFolderPaths.delete(node.path);
                            row.classList.remove('selected');
                        }
                        updateClearBtn();
                    });

                    frag.appendChild(row);

                    if (hasKids) {
                        const childrenWrap = document.createElement('div');
                        childrenWrap.className = 'thumb-folder-tree-children';
                        childrenWrap.style.display = 'none';
                        childrenWrap.appendChild(buildNodes(node.children, depth + 1));
                        frag.appendChild(childrenWrap);
                    }
                }
                return frag;
            }

            tree.appendChild(buildNodes(folders, 0));
        } catch (e) {
            console.warn('[设置] 加载文件夹列表失败:', e);
            tree.innerHTML = '<span style="font-size: 11px; color: var(--red); padding: 4px;">' + t('settings.load_failed') + '</span>';
        }
    }

    function toggleFolder(tree, row, hasKids) {
        if (!hasKids) return;
        const toggle = row.querySelector('.thumb-folder-tree-toggle');
        const children = row.nextElementSibling;
        if (!children || !children.classList.contains('thumb-folder-tree-children')) return;

        const isCollapsed = toggle.classList.contains('collapsed');
        if (isCollapsed) {
            toggle.classList.remove('collapsed');
            toggle.classList.add('expanded');
            children.style.display = '';
        } else {
            toggle.classList.add('collapsed');
            toggle.classList.remove('expanded');
            children.style.display = 'none';
        }
    }

    function updateClearBtn() {
        var has = _selectedFolderPaths.size > 0;
        var btn = document.getElementById('settingsClearFolderThumbs');
        if (btn) {
            btn.disabled = !has;
            btn.innerHTML = has ? '<span class="icon icon-delete"></span> ' + t('settings.clear_selection') + ' (' + _selectedFolderPaths.size + ')' : '<span class="icon icon-delete"></span> ' + t('settings.clear_folder_selection');
        }
        var btnStart = document.getElementById('settingsStartPreGen');
        if (btnStart) {
            btnStart.disabled = !has;
        }
    }

    async function clearSelectedFolderThumbs() {
        if (_selectedFolderPaths.size === 0) return;

        var folders = Array.from(_selectedFolderPaths);
        var folderList = folders.length <= 3 ? folders.join('\n') : folders.slice(0, 3).join('\n') + '\n...等 ' + folders.length + ' 个文件夹';
        if (!confirm(t('settings.confirm_clear_thumbs') + '\n\n' + folderList + '\n\n' + t('settings.clear_thumbs_note'))) return;

        var btn = document.getElementById('settingsClearFolderThumbs');
        btn.disabled = true;
        btn.innerHTML = '<span class="icon icon-loading"></span> ' + t('settings.cleaning');

        var totalCleaned = 0;
        var errors = [];
        for (var i = 0; i < folders.length; i++) {
            try {
                var result = await WailsBridge.clearFolderThumbs(folders[i]);
                if (result && result.success) {
                    totalCleaned += (result.cleaned || 0);
                } else {
                    errors.push((result && result.error) || t('settings.unknown_error'));
                }
            } catch (e) {
                errors.push(e.message);
            }
        }

        if (errors.length === 0) {
            App.showToast(t('settings.cleared_thumbs') + ': ' + totalCleaned, 'success');
        } else if (totalCleaned > 0) {
            App.showToast(t('settings.cleared_thumbs') + ': ' + totalCleaned + ', ' + errors.length + ' ' + t('settings.folders_failed'), 'warning');
        } else {
            App.showToast(t('settings.clean_failed') + ': ' + errors[0], 'error');
        }

        if (totalCleaned > 0 && typeof Gallery !== 'undefined' && Gallery.refreshThumbGen) {
            await Gallery.refreshThumbGen();
        }
        loadCacheStats();

        // 重新扫描所有选中的文件夹
        btn.innerHTML = '<span class="icon icon-loading"></span> ' + t('settings.scanning');
        for (var j = 0; j < folders.length; j++) {
            try {
                await WailsBridge.rescanFolder(folders[j]);
            } catch (scanErr) {
                console.warn('[设置] 重新扫描失败:', scanErr);
            }
        }
        if (typeof Sidebar !== 'undefined' && Sidebar.refreshFolderTree) {
            Sidebar.refreshFolderTree();
        }

        btn.disabled = false;
        updateClearBtn();
    }

    // ==================== 配色方案 ====================

    function initColorPickers() {
        const swatches = document.querySelectorAll('.color-swatch');
        const picker = document.getElementById('settingsAccentColor');
        const hexLabel = document.getElementById('colorHexLabel');

        // 获取当前配色
        const current = getCurrentAccentColor();

        // 高亮当前匹配的色块
        updateSwatchActive(swatches, current);
        if (picker) picker.value = current;
        if (hexLabel) hexLabel.textContent = current;

        // 色块点击
        swatches.forEach(swatch => {
            swatch.addEventListener('click', () => {
                const color = swatch.dataset.color;
                updateSwatchActive(swatches, color);
                if (picker) picker.value = color;
                if (hexLabel) hexLabel.textContent = color;
                saveAccentColor(color);
            });
        });

        // 自定义取色器
        if (picker) {
            picker.addEventListener('input', () => {
                const color = picker.value;
                updateSwatchActive(swatches, color);
                if (hexLabel) hexLabel.textContent = color;
                saveAccentColor(color);
            });
        }
    }

    function updateSwatchActive(swatches, color) {
        swatches.forEach(s => {
            s.classList.toggle('active', s.dataset.color.toLowerCase() === color.toLowerCase());
        });
    }

    function getCurrentAccentColor() {
        const style = getComputedStyle(document.documentElement);
        const accent = style.getPropertyValue('--accent').trim();
        return accent || '#e94560';
    }

    function saveAccentColor(color) {
        localStorage.setItem('accentColor', color);
        if (typeof Storage !== 'undefined' && Storage.setSetting) {
            Storage.setSetting('accentColor', color);
        }
        // 立即应用
        if (typeof App !== 'undefined' && App.applyAccentColor) {
            App.applyAccentColor(color);
        }
    }

    // ==================== 预生成缩略图 ====================

    function resetPreGenButtons() {
        var btnStart = document.getElementById('settingsStartPreGen');
        var btnPause = document.getElementById('settingsPausePreGen');
        var btnResume = document.getElementById('settingsResumePreGen');
        var btnStop = document.getElementById('settingsStopPreGen');
        var statusDiv = document.getElementById('settingsPreGenStatus');
        if (btnStart) { btnStart.style.display = ''; btnStart.disabled = _selectedFolderPaths.size === 0; }
        if (btnPause) btnPause.style.display = 'none';
        if (btnResume) btnResume.style.display = 'none';
        if (btnStop) btnStop.style.display = 'none';
        if (statusDiv) statusDiv.style.display = 'none';
    }

    function checkPreGenStatus() {
        if (typeof WailsBridge === 'undefined' || !WailsBridge.isWails()) return;
        WailsBridge.getPreGenStatus().then(function(s) {
            if (!s || !s.running) {
                resetPreGenButtons();
                return;
            }
            var btnStart = document.getElementById('settingsStartPreGen');
            var btnPause = document.getElementById('settingsPausePreGen');
            var btnResume = document.getElementById('settingsResumePreGen');
            var btnStop = document.getElementById('settingsStopPreGen');
            var statusDiv = document.getElementById('settingsPreGenStatus');
            if (btnStart) btnStart.style.display = 'none';
            if (btnStop) btnStop.style.display = '';
            if (statusDiv) statusDiv.style.display = '';
            if (s.paused) {
                if (btnPause) btnPause.style.display = 'none';
                if (btnResume) btnResume.style.display = '';
            } else {
                if (btnPause) btnPause.style.display = '';
                if (btnResume) btnResume.style.display = 'none';
            }
            updatePreGenUI(s);
        }).catch(function() {});
    }

    function updatePreGenUI(s) {
        var total = s.total || 0;
        var done = s.done || 0;
        var skipped = s.skipped || 0;
        var failed = s.failed || 0;
        var pct = total > 0 ? Math.round((done + skipped) / total * 100) : 0;

        var progress = document.getElementById('settingsPreGenProgress');
        var text = document.getElementById('settingsPreGenText');
        if (progress) progress.style.width = pct + '%';
        if (text) {
            var parts = [];
            if (total > 0) parts.push(done + skipped + ' / ' + total);
            if (s.paused) parts.push('[已暂停]');
            if (done > 0) parts.push(t('settings.generated') + ' ' + done);
            if (skipped > 0) parts.push(t('settings.skipped') + ' ' + skipped);
            if (failed > 0) parts.push(t('settings.failed') + ' ' + failed);
            text.textContent = parts.join('  ');
        }
    }

    function startPreGenPolling() {
        stopPreGenPolling();
        _preGenPollTimer = setInterval(function() {
            WailsBridge.getPreGenStatus().then(function(s) {
                if (!s || !s.running) {
                    stopPreGenPolling();
                    resetPreGenButtons();
                    if (s && s.total > 0) {
                        var msg = t('settings.done') + ' (' + t('settings.generated') + ' ' + s.done + ', ' + t('settings.skipped') + ' ' + s.skipped + (s.failed > 0 ? ', ' + t('settings.failed') + ' ' + s.failed : '') + ')';
                        if (typeof App !== 'undefined' && App.showToast) App.showToast(msg, 'success');
                    }
                    loadCacheStats();
                    return;
                }
                updatePreGenUI(s);
            }).catch(function() {
                stopPreGenPolling();
                resetPreGenButtons();
            });
        }, 500);
    }

    function stopPreGenPolling() {
        if (_preGenPollTimer) {
            clearInterval(_preGenPollTimer);
            _preGenPollTimer = null;
        }
    }

    function startPreGenPollingIfNeeded() {
        if (typeof WailsBridge === 'undefined' || !WailsBridge.isWails()) return;
        WailsBridge.getPreGenStatus().then(function(s) {
            if (s && s.running) startPreGenPolling();
        }).catch(function() {});
    }

    // ==================== 手机/平板访问 ====================

    async function loadLANInfo() {
        try {
            if (typeof WailsBridge !== 'undefined' && WailsBridge.isWails()) {
                const app = window.go.main.App;
                const info = await app.GetLANInfo();
                updateLANUI(info);
            }
        } catch (e) {
            // 非 Wails 环境或无权限
        }
        // 加载自动启动设置
        const autoStart = localStorage.getItem('lanAutoStart') === 'true';
        const autoStartCB = document.getElementById('settingsLanAutoStart');
        if (autoStartCB) autoStartCB.checked = autoStart;
        const savedPort = localStorage.getItem('lanPort');
        const portInput = document.getElementById('settingsLanPort');
        if (portInput && savedPort) portInput.value = savedPort;
    }

    let _lastLANInfo = null;

    function updateLANUI(info) {
        if (info !== undefined) _lastLANInfo = info;
        const data = _lastLANInfo;
        const startBtn = document.getElementById('settingsStartLAN');
        const stopBtn = document.getElementById('settingsStopLAN');
        const statusEl = document.getElementById('settingsLanStatus');
        const urlDiv = document.getElementById('settingsLanURL');
        const urlText = document.getElementById('settingsLanURLText');
        const ipInput = document.getElementById('settingsLanIP');

        if (data && data.running) {
            if (startBtn) startBtn.style.display = 'none';
            if (stopBtn) stopBtn.style.display = '';
            if (statusEl) statusEl.textContent = t("settings.wifi_running") || 'Running';
            if (statusEl) statusEl.style.color = 'var(--accent)';
            if (urlDiv) urlDiv.style.display = '';
            if (urlText) urlText.textContent = data.url || '';
            if (ipInput) ipInput.value = data.ip || '';
        } else {
            if (startBtn) startBtn.style.display = '';
            if (stopBtn) stopBtn.style.display = 'none';
            if (statusEl) statusEl.textContent = t("settings.wifi_stopped") || 'Stopped';
            if (statusEl) statusEl.style.color = 'var(--text-muted)';
            if (urlDiv) urlDiv.style.display = 'none';
            if (ipInput) ipInput.value = '';
        }
    }

    async function startLANServer() {
        const portInput = document.getElementById('settingsLanPort');
        const port = parseInt(portInput.value) || 25876;
        // 保存端口和自动启动设置
        localStorage.setItem('lanPort', String(port));
        localStorage.setItem('lanAutoStart', String(document.getElementById('settingsLanAutoStart').checked));

        try {
            if (typeof WailsBridge !== 'undefined' && WailsBridge.isWails()) {
                const app = window.go.main.App;
                const info = await app.StartLANServer(port);
                updateLANUI(info);
                App.showToast(t("settings.wifi_started_toast") || 'WiFi access started');
            }
        } catch (e) {
            App.showToast((t("settings.wifi_start_failed") || 'Failed to start') + ': ' + (e.message || String(e)), 'error');
        }
    }

    async function stopLANServer() {
        try {
            if (typeof WailsBridge !== 'undefined' && WailsBridge.isWails()) {
                const app = window.go.main.App;
                await app.StopLANServer();
                updateLANUI(null);
                App.showToast(t("settings.wifi_stopped_toast") || 'WiFi access stopped');
            }
        } catch (e) {
            App.showToast((t("settings.wifi_stop_failed") || 'Failed to stop') + ': ' + (e.message || String(e)), 'error');
        }
    }

    // ==================== 公开 API ====================

    return { init, loadLANInfo, startLANServer, stopLANServer };
})();
