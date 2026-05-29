/* ============================================================
   app.js - 主控制器
    整合所有模块，管理应用生命周期
    支持多根目录、本地文件夹导入、文件夹导航
   ============================================================ */

const App = (() => {
    const t = (typeof I18n !== 'undefined' ? I18n.t : (s) => s);
    // DOM
    let btnRefresh;

    // 状态
    let isInitialized = false;

    // ==================== 编辑模式 UI 更新 ====================

    function updateEditModeUI(enabled) {
        const btnEditMode = document.getElementById('btnEditMode');
        const editModeBanner = document.getElementById('editModeBanner');
        const galleryContainer = document.getElementById('galleryContainer');
        if (btnEditMode) {
            btnEditMode.classList.toggle('active', enabled);
            if (typeof t === 'function') {
                btnEditMode.innerHTML = enabled
                    ? '<span class="icon icon-edit"></span> ' + t('toolbar.edit_mode_exit')
                    : '<span class="icon icon-edit"></span> ' + t('toolbar.edit_mode');
            }
        }
        if (editModeBanner) {
            editModeBanner.classList.toggle('active', enabled);
        }
        if (galleryContainer) {
            galleryContainer.classList.toggle('edit-mode-active', enabled);
        }
    }

    // ==================== 初始化 ====================

    function detectMobile() {
        const ua = navigator.userAgent || '';
        // UA 中包含移动设备标识 或 屏幕宽度 ≤ 900px 且支持触摸
        const isMobileUA = /Mobi|Android|iPhone|iPad/i.test(ua);
        const isSmallTouch = window.innerWidth <= 900 && 'ontouchstart' in window;
        return isMobileUA || isSmallTouch;
    }

    async function init() {
        if (isInitialized) return;

        // 移动端 UA 检测（最先执行，让 CSS 尽早知道）
        if (detectMobile()) {
            document.body.classList.add('mobile');
        }

        // i18n 初始化（最先执���，确保 UI 文本正确）
        if (typeof I18n !== 'undefined') {
            await I18n.init();
        }

        // 主题初始化
        initTheme();

        // Phase 1: 存储初始化（Sidebar 数据加载的前置依赖）
        try {
            await Storage.init();
        } catch (err) {
            console.warn('[App] 存储初始化失败，部分功能可能不可用:', err.message);
            showToast(t('toast.storage_init_failed') + ': ' + err.message, 'warning');
        }

        // 加载自定义配色（需在 Storage.init 之后）
        await loadAccentColor();

        // 初始化图标路径（优先从 user/icons 目录加载，支持用户自定义）
        await initIcons();

        try {
            // Phase 2: 初始化各 UI 模块（同步，仅绑定 DOM 和回调）
            DetailPanel.init();
            ImageViewer.init();
            ImportExport.init();
            if (typeof Settings !== 'undefined') Settings.init();

            await Gallery.init({
                onImageClick: (imgData) => {
                    DetailPanel.showImage(imgData);
                },
                onSelectionChange: (selectedPaths) => {
                    // 选择变化时更新批量操作栏
                },
                onEditModeChange: (enabled) => {
                    updateEditModeUI(enabled);
                },
                onFolderTreeChange: () => {
                    Sidebar.refreshFolderTree();
                }
            });

            Sidebar.init({
                onFolderSelected: async () => {
                    // 文件夹过滤已在 Sidebar 内部处理
                },
                onTagSelected: async (tag) => {
                    await Gallery.filterByTag(tag.id);
                },
                onFilterFavorites: async (onlyFavorites) => {
                    await Gallery.filterByFavorites(onlyFavorites);
                },
                onBatchAction: async (action) => {
                    await handleBatchAction(action);
                }
            });

            // 移动端底部导航初始化
            initMobileNav();

            // 绑定全局事件（锁按钮、刷新、编辑模式等）
            bindGlobalEvents();

            // Phase 3: 并行加载——设置同步、导入信息恢复、侧边栏数据三者同时进行
            //   侧边栏数据和导入信息互不依赖，设置项之间也互不依赖
            const settingsSync = Promise.all([
                syncSetting('theme', applyTheme, 'theme'),
                syncSetting('leftPanelWidth', (v) => {
                    const w = parseInt(v, 10);
                    if (w >= 180 && w <= 600) {
                        if (document.body.classList.contains('mobile')) return;
                        const isLeftPanelCollapsed = localStorage.getItem('leftPanelCollapsed') === 'true';
                        const width = isLeftPanelCollapsed ? 0 : w;
                        document.documentElement.style.setProperty('--left-panel-width', width + 'px');
                        localStorage.setItem('leftPanelWidth', String(w));
                    }
                }, 'leftPanelWidth')
            ]);

            const importedRootsReady = (typeof Gallery !== 'undefined' && Gallery.loadImportedRootsFromServer)
                ? Gallery.loadImportedRootsFromServer().then(savedRoots => {
                    if (savedRoots.length > 0) {
                        console.log(`[App] 从后端恢复了 ${savedRoots.length} 个导入文件夹信息`);
                    }
                    return savedRoots;
                })
                : Promise.resolve([]);

            const sidebarReady = Promise.all([
                Sidebar.refreshFolderTree(),
                Sidebar.refreshTagTree(),
                Sidebar.refreshApiConfigSelect()
            ]);

            // 等待所有并行任务完成
            await Promise.all([settingsSync, importedRootsReady, sidebarReady]);

            console.log('[App] 等待用户选择本地文件夹...');

            isInitialized = true;
            console.log('[App] Local Gallery 初始化完成');

            // ★ 检查是否自动启动局域网服务
            checkLANAutoStart();

            // 启动后延迟刷新侧边栏，确保后台增量扫描的结果能反映到 UI
            setTimeout(() => {
                if (typeof Sidebar !== 'undefined' && Sidebar.refreshFolderTree) {
                    console.log('[App] 执行启动后延迟刷新');
                    Sidebar.refreshFolderTree();
                }
            }, 2500);
        } catch (err) {
            console.error('[App] 初始化失败:', err);
            showToast(t('toast.init_failed') + ': ' + err.message, 'error');
        }
    }

    // 单设置项同步辅助：服务器优先 → 回退 localStorage 并上传
    async function syncSetting(key, applyFn, localStorageKey) {
        try {
            const serverVal = await Storage.getSetting(key, null);
            if (serverVal != null) {
                applyFn(serverVal);
            } else {
                const localVal = localStorage.getItem(localStorageKey);
                if (localVal) {
                    applyFn(localVal);
                    await Storage.setSetting(key, isNaN(parseFloat(localVal)) ? localVal : parseFloat(localVal));
                }
            }
        } catch (e) { /* 静默 */ }
    }

    // ==================== 事件绑定 ====================

    function bindGlobalEvents() {
        btnRefresh = document.getElementById('btnRefresh');

        // ===== 锁定右侧面板按钮 =====
        const lockBtn = document.getElementById('lockRightPanel');
        if (lockBtn) {
            lockBtn.addEventListener('click', () => {
                const newLocked = !DetailPanel.getLocked();
                DetailPanel.setLocked(newLocked);
            });
        }

        // ===== 搜索模块初始化 =====
        if (typeof SearchModule !== 'undefined' && SearchModule.init) {
            SearchModule.init({
                onSearchResults: (galleryImages, query, total, append) => {
                    if (typeof Gallery !== 'undefined' && Gallery.displaySearchResults) {
                        Gallery.displaySearchResults(galleryImages, query, total, append);
                    }
                },
                onClearSearch: () => {
                    if (typeof Gallery !== 'undefined' && Gallery.clearSearchResults) {
                        Gallery.clearSearchResults();
                    }
                }
            });
        }

        // ===== 编辑模式 =====
        const btnEditMode = document.getElementById('btnEditMode');
        const btnExitEditMode = document.getElementById('btnExitEditMode');

        if (btnEditMode) {
            btnEditMode.addEventListener('click', () => {
                const newMode = Gallery.toggleEditMode();
                updateEditModeUI(newMode);
                if (newMode) {
                    showToast(t('toast.edit_mode_on'), 'info');
                } else {
                    showToast(t('toast.edit_mode_off'), 'info');
                }
            });
        }

        if (btnExitEditMode) {
            btnExitEditMode.addEventListener('click', () => {
                Gallery.setEditMode(false);
                updateEditModeUI(false);
                showToast(t('toast.edit_mode_off'), 'info');
            });
        }

        // 监听 Esc 键退出编辑模式
        document.addEventListener('keydown', (e) => {
            if (e.key === 'Escape' && Gallery.isEditMode && Gallery.isEditMode()) {
                Gallery.setEditMode(false);
                updateEditModeUI(false);
            }
        });

        // 全局刷新按钮：刷新整个 Wails 页面
        btnRefresh.addEventListener('click', () => {
            btnRefresh.classList.add('spinning');
            // 刷新页面，重新加载所有资源（包括自定义图标）
            window.location.reload();
        });

        // 窗口大小变化
        let resizeTimeout;
        let saveStateTimeout;
        window.addEventListener('resize', () => {
            clearTimeout(resizeTimeout);
            resizeTimeout = setTimeout(() => {
                Gallery.render();
            }, 200);
            // 保存窗口状态（更长防抖，避免频繁写入）
            clearTimeout(saveStateTimeout);
            saveStateTimeout = setTimeout(() => saveWindowState(), 500);
        });

        // 左侧面板拖拽调整宽度
        initPanelResizer();

        // 关闭前保存窗口状态
        window.addEventListener('beforeunload', () => {
            saveWindowState();
            const images = Gallery.getAllImages ? Gallery.getAllImages() : Gallery.getImages();
            for (const img of images) {
                if (img.thumbnailUrl && img.thumbnailUrl.startsWith('blob:')) {
                    URL.revokeObjectURL(img.thumbnailUrl);
                }
            }
        });
    }

    // ==================== 主题管理 ====================

    function initTheme() {
        const saved = localStorage.getItem('theme');
        const prefersDark = window.matchMedia('(prefers-color-scheme: dark)').matches;
        const theme = saved || (prefersDark ? 'dark' : 'light');
        applyTheme(theme);

        const btn = document.getElementById('btnToggleTheme');
        if (btn) {
            btn.addEventListener('click', toggleTheme);
        }

        // 语言切换后刷新页面，确保所有动态渲染的 UI 使用新语言
        const langSelect = document.getElementById('langSelect');
        if (langSelect && typeof I18n !== 'undefined') {
            langSelect.value = I18n.lang();
            langSelect.addEventListener('change', async () => {
                await I18n.setLang(langSelect.value);
                location.reload();
            });
        }

        // 监听系统主题变化
        window.matchMedia('(prefers-color-scheme: dark)').addEventListener('change', (e) => {
            if (!localStorage.getItem('theme')) {
                applyTheme(e.matches ? 'dark' : 'light');
            }
        });
    }

    function applyTheme(theme) {
        document.documentElement.setAttribute('data-theme', theme);
        const btn = document.getElementById('btnToggleTheme');
        if (btn) {
            btn.innerHTML = theme === 'dark' ? '<span class="icon icon-theme-dark"></span>' : '<span class="icon icon-theme-light"></span>';
        }
    }

    // ==================== 图标初始化 ====================

    // 所有系统图标名称（与 static/icons/ 中的文件名及 CSS 类名一一对应）
    const SYSTEM_ICONS = [
        'settings', 'reload', 'theme-dark', 'theme-light', 'search', 'close',
        'folder', 'tag', 'api',
        'browse', 'delete', 'edit', 'add', 'favorite-on', 'favorite-off',
        'copy', 'save', 'plug', 'template', 'import', 'export', 'reset', 'columns',
        'check', 'loading', 'warning', 'lock', 'unlock', 'clean', 'broken', 'none', 'image',
        'expand', 'collapse', 'menu', 'back',
        'art', 'user', 'bot', 'file', 'web', 'random', 'processing', 'numbered', 'click', 'stop',
        'grid', 'masonry', 'pinterest', 'list', 'index', 'indexing', 'index-done'
    ];

    async function initIcons() {
        let baseURL = '';
        // 重试逻辑：Go 后端可能还没就绪
        for (let retry = 0; retry < 3; retry++) {
            try {
                baseURL = await WailsBridge.getHTTPBaseURL();
                if (baseURL) break;
            } catch (e) {
                console.warn('[Icons] 获取 HTTP 服务器地址失败 (attempt ' + (retry+1) + '/3):', e);
            }
            if (retry < 2) await new Promise(r => setTimeout(r, 500));
        }
        if (!baseURL) {
            console.warn('[Icons] 无法获取 HTTP 服务器地址，使用默认嵌入图标');
            return;
        }

        const timestamp = Date.now();

        // 方法1: 注入 CSS (带时间戳防缓存)
        let css = '';
        for (const name of SYSTEM_ICONS) {
            css += `.icon-${name}{background-image:url(${baseURL}/icons/icon-${name}.svg?t=${timestamp})!important}\n`;
        }
        const style = document.createElement('style');
        style.id = 'icon-overrides';
        style.textContent = css;
        document.head.appendChild(style);

        // 方法2: 直接遍历所有图标元素强制设置 background-image (确保生效)
        for (const name of SYSTEM_ICONS) {
            const elements = document.querySelectorAll(`.icon-${name}`);
            elements.forEach(el => {
                el.style.backgroundImage = `url(${baseURL}/icons/icon-${name}.svg?t=${timestamp})`;
            });
        }

        console.log('[Icons] 图标自定义已启用，baseURL:', baseURL);
    }

    // ==================== 配色管理 ====================

    function hexToRgb(hex) {
        const result = /^#?([a-f\d]{2})([a-f\d]{2})([a-f\d]{2})$/i.exec(hex);
        return result ? [parseInt(result[1], 16), parseInt(result[2], 16), parseInt(result[3], 16)] : null;
    }

    function applyAccentColor(hex) {
        const rgb = hexToRgb(hex);
        if (!rgb) return;
        const [r, g, b] = rgb;

        // 根据当前主题计算 hover 和 glow
        const isDark = (document.documentElement.getAttribute('data-theme') || 'dark') === 'dark';
        // HSL 亮度调整
        const factor = isDark ? 1.15 : 0.85;
        const hr = Math.min(255, Math.round(r * factor + (isDark ? 40 : -30)));
        const hg = Math.min(255, Math.round(g * factor + (isDark ? 40 : -30)));
        const hb = Math.min(255, Math.round(b * factor + (isDark ? 40 : -30)));
        const glowAlpha = isDark ? 0.25 : 0.15;

        document.documentElement.style.setProperty('--accent', hex);
        document.documentElement.style.setProperty('--accent-hover', `rgb(${hr},${hg},${hb})`);
        document.documentElement.style.setProperty('--accent-glow', `rgba(${r},${g},${b},${glowAlpha})`);

        // 同步更新主题切换按钮等需要适配的元素
        const tocIcon = document.getElementById('btnToggleTheme');
        // 保持主题按钮不受影响
    }

    async function loadAccentColor() {
        try {
            if (typeof Storage !== 'undefined' && Storage.getSetting) {
                const saved = await Storage.getSetting('accentColor', null);
                if (saved) {
                    applyAccentColor(saved);
                    return;
                }
            }
        } catch (e) { /* 静默 */ }
        const local = localStorage.getItem('accentColor');
        if (local) {
            applyAccentColor(local);
        }
    }

    function toggleTheme() {
        const current = document.documentElement.getAttribute('data-theme') || 'dark';
        const next = current === 'dark' ? 'light' : 'dark';
        localStorage.setItem('theme', next);
        if (typeof Storage !== 'undefined' && Storage.setSetting) {
            Storage.setSetting('theme', next);
        }
        applyTheme(next);
    }

    // ==================== 窗口状态保存 ====================

    function saveWindowState() {
        try {
            const w = window.innerWidth;
            const h = window.innerHeight;
            const maximised = w >= screen.availWidth - 10 && h >= screen.availHeight - 10;
            const x = window.screenX || window.screenLeft || 0;
            const y = window.screenY || window.screenTop || 0;
            if (maximised || x < -10000 || y < -10000) {
                return;
            }

            if (window.go && window.go.main && window.go.main.App) {
                window.go.main.App.SaveWindowState(w, h, x, y, false);
            }
        } catch (e) {
            // 静默失败，窗口状态保存不影响功能
        }
    }

    // ==================== 移动端底部导航 ====================

    function initMobileNav() {
        if (!document.body.classList.contains('mobile')) return;

        const overlay = document.getElementById('panelOverlay');
        const leftPanel = document.getElementById('leftPanel');
        const rightPanel = document.getElementById('rightPanel');
        const btnFolder = document.getElementById('mobileNavFolder');
        const btnInfo = document.getElementById('mobileNavInfo');

        if (!overlay || !btnFolder || !btnInfo) return;

        let openPanel = null; // 'left' | 'right' | null

        function open(panel) {
            close();
            openPanel = panel;
            if (panel === 'left') {
                leftPanel && leftPanel.classList.add('mobile-open');
                btnFolder && btnFolder.classList.add('active');
            } else {
                rightPanel && rightPanel.classList.add('mobile-open');
                btnInfo && btnInfo.classList.add('active');
            }
            overlay.classList.add('show');
        }

        function close() {
            openPanel = null;
            leftPanel && leftPanel.classList.remove('mobile-open');
            rightPanel && rightPanel.classList.remove('mobile-open');
            btnFolder && btnFolder.classList.remove('active');
            btnInfo && btnInfo.classList.remove('active');
            overlay.classList.remove('show');
        }

        function toggle(panel) {
            if (openPanel === panel) {
                close();
            } else {
                open(panel);
            }
        }

        btnFolder.addEventListener('click', () => toggle('left'));
        btnInfo.addEventListener('click', () => toggle('right'));
        overlay.addEventListener('click', close);
    }

    // ==================== 面板分割条拖拽 ====================

    function initPanelResizer() {
        const resizer = document.getElementById('leftResizer');
        const leftPanel = document.getElementById('leftPanel');
        const root = document.documentElement;
        if (!resizer || !leftPanel) return;

        const MIN_WIDTH = 180;
        const MAX_WIDTH = 600;

        // 恢复上次保存的面板宽度（通过 CSS 变量驱动）
        const savedWidth = localStorage.getItem('leftPanelWidth');
        if (savedWidth) {
            const w = parseInt(savedWidth, 10);
            if (w >= MIN_WIDTH && w <= MAX_WIDTH) {
                root.style.setProperty('--left-panel-width', w + 'px');
            }
        }

        let dragging = false;
        let startX = 0;
        let startWidth = 0;
        let targetWidth = 0;
        let ghostLine = null;
        let panelLeft = 0; // leftPanel 左边缘的视口 X 坐标（拖拽开始时捕获）

        // 幽灵竖线：position:fixed + transform 移动，纯 GPU 合成，零重排
        function createGhostLine(x) {
            if (ghostLine) return;
            ghostLine = document.createElement('div');
            ghostLine.className = 'panel-resize-line';
            ghostLine.style.left = x + 'px';
            document.body.appendChild(ghostLine);
        }
        function removeGhostLine() {
            if (ghostLine) {
                ghostLine.remove();
                ghostLine = null;
            }
        }

        resizer.addEventListener('pointerdown', (e) => {
            dragging = true;
            startX = e.clientX;
            startWidth = parseInt(getComputedStyle(leftPanel).width, 10);
            targetWidth = startWidth;
            panelLeft = leftPanel.getBoundingClientRect().left;
            document.body.classList.add('is-resizing');
            resizer.setPointerCapture(e.pointerId);
            // 在分割线当前位置创建幽灵竖线
            createGhostLine(panelLeft + startWidth);
        });

        // pointermove: 只移动幽灵竖线 (transform，GPU 合成层，完全不触发 reflow)
        resizer.addEventListener('pointermove', (e) => {
            if (!dragging || !ghostLine) return;
            const dx = e.clientX - startX;
            targetWidth = Math.max(MIN_WIDTH, Math.min(MAX_WIDTH, startWidth + dx));
            // transform 移动 = compositor-only，0 layout 0 paint
            ghostLine.style.transform = `translateX(${targetWidth - startWidth}px)`;
        });

        function endDrag() {
            if (!dragging) return;
            dragging = false;
            document.body.classList.remove('is-resizing');
            removeGhostLine();
            // 拖拽结束，一次性应用最终宽度 —— 唯一一次完整 reflow
            root.style.setProperty('--left-panel-width', targetWidth + 'px');
            localStorage.setItem('leftPanelWidth', Math.round(targetWidth));
            if (typeof Storage !== 'undefined' && Storage.setSetting) {
                Storage.setSetting('leftPanelWidth', Math.round(targetWidth));
            }
            // 触发图库重绘以修正布局
            if (typeof Gallery !== 'undefined' && Gallery.render) {
                Gallery.render();
            }
        }

        resizer.addEventListener('pointerup', endDrag);
        resizer.addEventListener('pointercancel', endDrag);
    }

    // ==================== 批量操作处理 ====================

    async function handleBatchAction(action) {
        switch (action) {
            case 'tag':
                await showBatchTagDialog();
                break;
            case 'untag':
                // 如果在标签视图中，直接对应当前标签操作，无需选择标签
                const currentTag = Gallery.getCurrentTagFilter ? Gallery.getCurrentTagFilter() : null;
                if (currentTag) {
                    await Gallery.batchRemoveTag(currentTag);
                } else {
                    await showBatchUntagDialog();
                }
                break;
            case 'favorite':
                await Gallery.batchToggleFavorite();
                break;
            case 'unfavorite':
                await Gallery.batchRemoveFavorite();
                break;
            case 'reverse':
                Gallery.startBatchReverse();
                break;
            case 'clear':
                Gallery.clearSelection();
                break;
        }
    }

    async function showBatchTagDialog() {
        const tags = await Storage.getAllTags();
        if (tags.length === 0) {
            showToast(t('toast.create_tag_first'), 'warning');
            return;
        }

        const overlay = document.getElementById('modalOverlay');
        const content = document.getElementById('modalContent');

        let tagOptions = tags.map(t =>
            `<option value="${t.id}">${t.name}</option>`
        ).join('');

        content.innerHTML = `
            <h2><span class="icon icon-tag"></span> 批量添加标签</h2>
            <p style="color: var(--text-secondary); margin-bottom: 12px;">
                为选中的 <strong style="color: var(--accent);">${Gallery.getSelectedImages().length}</strong> 张图片添加标签
            </p>
            <div class="form-group">
                <label>选择标签</label>
                <select id="batchTagSelect" style="width:100%;padding:8px;background:var(--bg-input);border:1px solid var(--border-color);border-radius:var(--radius-sm);color:var(--text-primary);">
                    ${tagOptions}
                </select>
            </div>
            <div class="modal-actions">
                <button id="btnCancelBatchTag" class="btn-secondary">取消</button>
                <button id="btnConfirmBatchTag" class="btn-primary">确认添加</button>
            </div>
        `;

        overlay.style.display = 'flex';

        document.getElementById('btnCancelBatchTag').addEventListener('click', () => {
            overlay.style.display = 'none';
        });

        document.getElementById('btnConfirmBatchTag').addEventListener('click', async () => {
            overlay.style.display = 'none';
            const tagId = document.getElementById('batchTagSelect').value;
            if (tagId) {
                await Gallery.batchAddTag(tagId);
            }
        });

        overlay.addEventListener('click', (e) => {
            if (e.target === overlay) {
                overlay.style.display = 'none';
            }
        });
    }

    async function showBatchUntagDialog() {
        const tags = await Storage.getAllTags();
        if (tags.length === 0) {
            showToast(t('toast.no_tags_to_remove'), 'warning');
            return;
        }

        const overlay = document.getElementById('modalOverlay');
        const content = document.getElementById('modalContent');

        let tagOptions = tags.map(t =>
            `<option value="${t.id}">${t.name}</option>`
        ).join('');

        content.innerHTML = `
            <h2><span class="icon icon-close"></span> 批量移除标签</h2>
            <p style="color: var(--text-secondary); margin-bottom: 12px;">
                从选中的 <strong style="color: var(--accent);">${Gallery.getSelectedImages().length}</strong> 张图片中移除标签
            </p>
            <div class="form-group">
                <label>选择要移除的标签</label>
                <select id="batchUntagSelect" style="width:100%;padding:8px;background:var(--bg-input);border:1px solid var(--border-color);border-radius:var(--radius-sm);color:var(--text-primary);">
                    ${tagOptions}
                </select>
            </div>
            <div class="modal-actions">
                <button id="btnCancelBatchUntag" class="btn-secondary">取消</button>
                <button id="btnConfirmBatchUntag" class="btn-primary btn-danger">确认移除</button>
            </div>
        `;

        overlay.style.display = 'flex';

        document.getElementById('btnCancelBatchUntag').addEventListener('click', () => {
            overlay.style.display = 'none';
        });

        document.getElementById('btnConfirmBatchUntag').addEventListener('click', async () => {
            overlay.style.display = 'none';
            const tagId = document.getElementById('batchUntagSelect').value;
            if (tagId) {
                await Gallery.batchRemoveTag(tagId);
            }
        });

        overlay.addEventListener('click', (e) => {
            if (e.target === overlay) {
                overlay.style.display = 'none';
            }
        });
    }

    // ==================== Toast 通知 ====================

    function showToast(message, type = 'info') {
        // Try I18n translation; I18n.t returns the key itself when not found
        const translated = (typeof I18n !== 'undefined') ? I18n.t(message) : message;
        const container = document.getElementById('toastContainer');
        const toast = document.createElement('div');
        toast.className = `toast ${type}`;
        toast.textContent = translated;
        container.appendChild(toast);

        setTimeout(() => {
            if (toast.parentNode) {
                toast.remove();
            }
        }, 3000);
    }

    // ==================== 局域网服务自动启动 ====================

    function checkLANAutoStart() {
        if (localStorage.getItem('lanAutoStart') !== 'true') return;
        const port = parseInt(localStorage.getItem('lanPort')) || 25876;
        try {
            if (typeof window.go !== 'undefined' && window.go.main && window.go.main.App) {
                window.go.main.App.StartLANServer(port).catch(() => {});
            }
        } catch (_) {}
    }

    // ==================== 公开 API ====================

    return {
        init,
        showToast,
        applyAccentColor
    };
})();

// ==================== 启动应用 ====================

document.addEventListener('DOMContentLoaded', () => {
    App.init();
});
