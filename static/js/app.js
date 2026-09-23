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
    const ASSET_VER = 'u13'; // 前端资源版本（与 index.html ?v= 同步更新）

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

        // ★ 启动打点：DOMContentLoaded（近似窗口显示时间）
        if (window.go && window.go.main && window.go.main.App && window.go.main.App.LogStartupTiming) {
            window.go.main.App.LogStartupTiming('DOMContentLoaded');
        }

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

        // Phase 1: 启动并行任务（不阻塞 UI 模块初始化）
        const storageReady = Storage.init().catch(err => {
            console.warn('[App] 存储初始化失败，部分功能可能不可用:', err.message);
            showToast(t('toast.storage_init_failed') + ': ' + err.message, 'warning');
        });
        // 配色加载依赖 Storage.init，但不阻塞主流程
        const accentReady = storageReady.then(() => loadAccentColor()).catch(() => {});
        // 图标初始化 fire-and-forget（失败有默认图标兜底）
        initIcons().catch(err => console.warn('[Icons] 初始化失败:', err));

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

            console.log('[App] 前端资源版本: ' + ASSET_VER + ' (css/js ?v=2.15)');
// 移动端底部导航初始化
            initMobileNav();
            // 移动端画廊工具栏下拉初始化
            initMobileToolbar();
            // 移动端顶栏下拉初始化（设置/刷新/主题/高级搜索/生成参数收进菜单）
            initMobileTopbar();

            // 绑定全局事件（锁按钮、刷新、编辑模式等）
            bindGlobalEvents();

            // ★ 提前注册 index:ready：后端 ensureImageIndex 可能在下面 await 完成前
            //   就发出该事件。若等初始化结束才注册，事件会被错过 → 侧栏树停在启动
            //   竞态的空/旧状态（已注册的文件夹不显示，需手动重扫才恢复）。
            if (window.runtime && window.runtime.EventsOn) {
                window.runtime.EventsOn('index:ready', () => {
                    if (typeof Sidebar !== 'undefined' && Sidebar.refreshFolderTree) {
                        console.log('[App] 收到 index:ready，刷新侧栏');
                        Sidebar.refreshFolderTree();
                    }
                });
            }

            // Phase 3: 侧边栏数据立即并行启动（不依赖 Storage），其他设置同步等 Storage 就绪
            const sidebarReady = Promise.all([
                Sidebar.refreshFolderTree(),
                // ★ 修复：标签数据来自 Storage（serverDataCache.tags），而 Storage 需
                //   先从后端 syncFromServer 加载。若不等待 storageReady，refreshTagTree
                //   会读到空的 tags → 标签栏空白；且 sync 完成后没有事件触发重渲染，
                //   直到用户"创建新标签"才重新渲染（表现为"点一下才出现"）。
                storageReady.then(() => Sidebar.refreshTagTree()),
                Sidebar.refreshApiConfigSelect()
            ]);

            const settingsSync = storageReady.then(() => Promise.all([
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
            ]));

            const importedRootsReady = (typeof Gallery !== 'undefined' && Gallery.loadImportedRootsFromServer)
                ? Gallery.loadImportedRootsFromServer().then(savedRoots => {
                    if (savedRoots.length > 0) {
                        console.log(`[App] 从后端恢复了 ${savedRoots.length} 个导入文件夹信息`);
                    }
                    return savedRoots;
                })
                : Promise.resolve([]);

            // 等待所有并行任务完成
            await Promise.all([settingsSync, importedRootsReady, sidebarReady, accentReady]);

            // ★ 恢复上次浏览的文件夹（手机端重开页面直接回到原位置）
            try {
                if (typeof Gallery !== 'undefined' && Gallery.filterByFolder && typeof Storage !== 'undefined' && Storage.getSetting) {
                    const lastFolder = await Storage.getSetting('lastFolder', null);
                    if (lastFolder) {
                        console.log('[App] 恢复上次浏览的文件夹:', lastFolder);
                        Gallery.filterByFolder(lastFolder, '', {}).catch(err => {
                            console.warn('[App] 恢复上次文件夹失败:', err.message);
                        });
                    }
                }
            } catch (e) { /* 静默，不影响启动 */ }

            console.log('[App] 等待用户选择本地文件夹...');

            isInitialized = true;
            console.log('[App] Local Gallery 初始化完成');
            // ★ 启动打点：前端初始化完成
            if (window.go && window.go.main && window.go.main.App && window.go.main.App.LogStartupTiming) {
                window.go.main.App.LogStartupTiming('init-done');
            }

            // ★ 检查是否自动启动局域网服务
            checkLANAutoStart();

            // ★ 兜底刷新侧栏：后端 ensureImageIndex 可能已在初始化 await 之前完成，
            //   若其 index:ready 事件被错过，侧栏树可能停留在启动竞态的空/旧状态
            //   （表现为"已注册的文件夹重启后不显示"）。这里无条件再刷新一次，
            //   数据未变时 renderFolderTree 的 hash 比对会跳过重建，开销极小。
            if (typeof Sidebar !== 'undefined' && Sidebar.refreshFolderTree) {
                Sidebar.refreshFolderTree();
            }
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

        // 全局刷新按钮：仅重载前端页面，不做后端扫盘。
        // 扫盘/增量刷新请使用文件夹右键"刷新"、导入或详情面板中的对应入口，
        // 避免大图库刷新（逐根目录走盘+落库）期间阻塞浏览。
        btnRefresh.addEventListener('click', () => {
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

        // ★ 语言切换：重新应用静态翻译 + 刷新动态 UI
        window.addEventListener('i18n:changed', () => {
            if (typeof I18n !== 'undefined' && I18n.scanDOM) I18n.scanDOM();
            if (typeof Sidebar !== 'undefined' && Sidebar.refreshTagTree) Sidebar.refreshTagTree();
            if (typeof Sidebar !== 'undefined' && Sidebar.refreshFolderTree) Sidebar.refreshFolderTree();
            if (typeof DetailPanel !== 'undefined' && DetailPanel.refreshI18n) DetailPanel.refreshI18n();
        });
    }

    // ==================== 主题管理 ====================

    // 主题注册表：dark/light 为「单色模式」，其余为扩展主题
    const THEMES = [
        { id: 'dark', nameKey: 'theme.mono_dark', dark: true },
        { id: 'light', nameKey: 'theme.mono_light', dark: false },
        { id: 'gallery-dark', nameKey: 'theme.gallery_dark', dark: true },
        { id: 'gallery-light', nameKey: 'theme.gallery_light', dark: false },
        { id: 'arctic-aurora', nameKey: 'theme.arctic_aurora', dark: true, decor: true },
        { id: 'egyptian-museum', nameKey: 'theme.egyptian_museum', dark: true, decor: true },
        { id: 'watercolor-doodle', nameKey: 'theme.watercolor_doodle', dark: true, decor: true }
    ];

    const THEME_COLORS = {
        'dark': '#1a1a1a',
        'light': '#fafbfc',
        'gallery-dark': '#101014',
        'gallery-light': '#FBFBFC',
        'arctic-aurora': '#0A0E14',
        'egyptian-museum': '#0E0C09',
        'watercolor-doodle': '#171522'
    };

    function isKnownTheme(id) {
        return THEMES.some(t => t.id === id);
    }

    function isDarkTheme(id) {
        const t = THEMES.find(t => t.id === id);
        return t ? t.dark : true;
    }

    function initTheme() {
        const saved = localStorage.getItem('theme');
        const prefersDark = window.matchMedia('(prefers-color-scheme: dark)').matches;
        const theme = isKnownTheme(saved) ? saved : (prefersDark ? 'dark' : 'light');
        applyTheme(theme);

        const btn = document.getElementById('btnToggleTheme');
        if (btn) {
            btn.addEventListener('click', cycleTheme);
        }

        // 语言切换：不依赖 location.reload()（Wails WebView 中可能不可靠），
        // 改为 I18n.setLang 派发 i18n:changed 事件，各模块监听后刷新动态文本。
        const langSelect = document.getElementById('langSelect');
        if (langSelect && typeof I18n !== 'undefined') {
            langSelect.value = I18n.lang();
            langSelect.addEventListener('change', async () => {
                await I18n.setLang(langSelect.value);
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
        if (!isKnownTheme(theme)) theme = 'dark';
        document.documentElement.setAttribute('data-theme', theme);

        // 新主题隐藏带文字按钮上的图标（纯图标按钮保留）
        const isMono = theme === 'dark' || theme === 'light';
        document.body.classList.toggle('theme-no-btn-icons', !isMono);

        // 装饰层：仅装饰主题显示，极光主题需要极光带子元素
        const decor = document.getElementById('themeDecor');
        if (decor) {
            decor.innerHTML = theme === 'arctic-aurora'
                ? '<i class="aur a1"></i><i class="aur a2"></i>'
                : '';
        }

        // 浏览器/窗口边框配色
        let meta = document.querySelector('meta[name="theme-color"]');
        if (!meta) {
            meta = document.createElement('meta');
            meta.name = 'theme-color';
            document.head.appendChild(meta);
        }
        meta.content = THEME_COLORS[theme] || '#1a1a1a';

        const btn = document.getElementById('btnToggleTheme');
        if (btn) {
            btn.innerHTML = isDarkTheme(theme)
                ? '<span class="icon icon-theme-dark"></span>'
                : '<span class="icon icon-theme-light"></span>';
            if (typeof I18n !== 'undefined' && I18n.t) {
                btn.title = I18n.t('settings.theme') + ' · ' + I18n.t(THEMES.find(t => t.id === theme).nameKey);
            }
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
        // 优先用 WailsBridge 启动时已预热的缓存，避免重试 RPC
        if (typeof WailsBridge !== 'undefined' && WailsBridge._httpBaseURL) {
            baseURL = WailsBridge._httpBaseURL;
        } else {
            try {
                baseURL = await WailsBridge.getHTTPBaseURL();
            } catch (e) {
                console.warn('[Icons] 获取 HTTP 服务器地址失败:', e);
            }
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
        const isDark = isDarkTheme(document.documentElement.getAttribute('data-theme') || 'dark');
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

    // 清除自定义强调色,恢复主题自带配色
    function resetAccentColor() {
        localStorage.removeItem('accentColor');
        if (typeof Storage !== 'undefined' && Storage.setSetting) {
            Storage.setSetting('accentColor', null);
        }
        const rootStyle = document.documentElement.style;
        rootStyle.removeProperty('--accent');
        rootStyle.removeProperty('--accent-hover');
        rootStyle.removeProperty('--accent-glow');
    }

    function applyThemeById(theme) {
        localStorage.setItem('theme', theme);
        if (typeof Storage !== 'undefined' && Storage.setSetting) {
            Storage.setSetting('theme', theme);
        }
        // 切换主题时清除自定义强调色,使用新主题的设计配色
        resetAccentColor();
        applyTheme(theme);
    }

    function cycleTheme() {
        const current = document.documentElement.getAttribute('data-theme') || 'dark';
        const idx = THEMES.findIndex(t => t.id === current);
        applyThemeById(THEMES[(idx + 1) % THEMES.length].id);
    }

    function getThemeInfo() {
        const current = document.documentElement.getAttribute('data-theme') || 'dark';
        return {
            current,
            themes: THEMES.map(t => ({ id: t.id, nameKey: t.nameKey, dark: t.dark }))
        };
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
        const btnSort = document.getElementById('mobileNavSort');
        const btnLock = document.getElementById('mobileNavLock');

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

        // ★ 排序按钮：弹出排序列表选择（不再点击循环切换）
        if (btnSort) {
            const sortMenu = document.getElementById('mobileSortMenu');
            const closeSortMenu = () => { if (sortMenu) sortMenu.style.display = 'none'; };
            const openSortMenu = () => {
                const sel = document.getElementById('sortOrder');
                if (!sel || !sortMenu || sel.options.length === 0) return;
                // 从桌面端下拉的选项构建列表（标签已按当前语言翻译）
                sortMenu.innerHTML = '';
                Array.from(sel.options).forEach(opt => {
                    const item = document.createElement('button');
                    item.type = 'button';
                    item.className = 'mobile-sort-option' + (opt.value === sel.value ? ' active' : '');
                    item.textContent = opt.textContent || opt.value;
                    item.addEventListener('click', () => {
                        sel.value = opt.value;
                        sel.dispatchEvent(new Event('change', { bubbles: true }));
                        closeSortMenu();
                        if (typeof App !== 'undefined' && App.showToast) {
                            App.showToast(opt.textContent || opt.value, 'info');
                        }
                    });
                    sortMenu.appendChild(item);
                });
                sortMenu.style.display = 'block';
            };
            btnSort.addEventListener('click', (e) => {
                e.stopPropagation();
                if (sortMenu && sortMenu.style.display === 'block') {
                    closeSortMenu();
                } else {
                    openSortMenu();
                }
            });
            // 点击其它区域关闭排序菜单
            document.addEventListener('click', (e) => {
                if (sortMenu && sortMenu.style.display === 'block' && !sortMenu.contains(e.target)) {
                    closeSortMenu();
                }
            });
            // 打开抽屉时也关闭排序菜单
            btnFolder.addEventListener('click', closeSortMenu);
            btnInfo.addEventListener('click', closeSortMenu);
        }
        // ★ 锁定按钮：切换右侧信息面板锁定状态（复用 DetailPanel.setLocked 逻辑）
        if (btnLock && typeof DetailPanel !== 'undefined') {
            const syncLockBtn = () => {
                const locked = !!DetailPanel.getLocked();
                btnLock.classList.toggle('active', locked);
                const icon = btnLock.querySelector('.icon');
                if (icon) icon.className = 'icon ' + (locked ? 'icon-lock' : 'icon-unlock');
            };
            btnLock.addEventListener('click', () => {
                DetailPanel.setLocked(!DetailPanel.getLocked());
                syncLockBtn();
            });
            syncLockBtn();
        }
    }

    // ==================== 移动端画廊工具栏下拉 ====================

    function initMobileToolbar() {
        if (!document.body.classList.contains('mobile')) return;
        if (document.getElementById('mobileToolbarToggle')) return; // 已初始化

        const toolbarLeft = document.querySelector('.gallery-toolbar .toolbar-left');
        const toolbarRight = document.querySelector('.gallery-toolbar .toolbar-right');
        const gallery = document.getElementById('galleryContainer');
        if (!toolbarLeft || !toolbarRight || !gallery) return;

        // 下拉开关按钮（放在排序下拉左侧）
        const toggle = document.createElement('button');
        toggle.id = 'mobileToolbarToggle';
        toggle.className = 'edit-mode-btn';
        toggle.title = t('toolbar.more_tools') || '更多工具';
        toggle.innerHTML = '<span class="icon icon-sliders"></span><span class="dropdown-arrow">&#9662;</span>';
        toolbarRight.insertBefore(toggle, toolbarRight.firstChild);

        // 下拉菜单容器
        const menu = document.createElement('div');
        menu.id = 'mobileToolbarMenu';
        menu.className = 'mobile-toolbar-menu';
        menu.style.display = 'none';

        // 把次要控件搬进下拉菜单（DOM 节点移动保留事件绑定）；
        // 工具栏上只留布局切换、工具入口与排序下拉
        const movers = [
            toolbarLeft.querySelector('.thumbnail-slider'),
            document.getElementById('btnEditMode'),
            document.getElementById('btnInertiaToggle'),
            document.getElementById('btnInertiaSettings'),
            document.getElementById('imageCount'),
            document.getElementById('btnShowPromptCount'),
            document.getElementById('lockRightPanel'),
        ];
        movers.forEach(el => { if (el) menu.appendChild(el); });
        gallery.appendChild(menu);

        // 纯图标按钮在菜单里补文字标签（保留 data-i18n 以便切换语言时更新）
        const menuLabels = {
            btnInertiaSettings: 'inertia.title',
            btnShowPromptCount: 'toolbar.show_prompt_count',
            lockRightPanel: 'mobile.lock',
        };
        Object.entries(menuLabels).forEach(([id, key]) => {
            const btn = document.getElementById(id);
            if (!btn || btn.querySelector('.menu-label')) return;
            const span = document.createElement('span');
            span.className = 'menu-label';
            span.setAttribute('data-i18n', key);
            // i18n 字典可能被缓存而缺少新键，用按钮 title 的首段做兜底
            const translated = t(key);
            span.textContent = translated !== key ? translated
                : (key === 'mobile.lock' ? '锁定' : (btn.title || key).split(/[：:]/)[0]);
            btn.appendChild(span);
        });

        const close = () => { menu.style.display = 'none'; toggle.classList.remove('active'); };
        toggle.addEventListener('click', (e) => {
            e.stopPropagation();
            if (menu.style.display === 'none') {
                menu.style.display = 'flex';
                toggle.classList.add('active');
            } else {
                close();
            }
        });
        // 点击菜单外区域关闭
        document.addEventListener('click', (e) => {
            if (menu.style.display !== 'none' && !menu.contains(e.target)) close();
        });
        // 打开抽屉时也关闭
        const overlay = document.getElementById('panelOverlay');
        if (overlay) overlay.addEventListener('click', close);
    }
    // 供测试/调试在 mobile 类后挂时手动触发
    window.__initMobileToolbar = initMobileToolbar;

    // ==================== 移动端顶栏下拉 ====================
    // 顶栏右侧按钮（设置/刷新/主题）与搜索区动作按钮（高级搜索/生成参数）
    // 收进一个"更多"下拉菜单，避免窄屏上互相挤压遮挡。
    function initMobileTopbar() {
        if (!document.body.classList.contains('mobile')) return;
        if (document.getElementById('mobileTopbarToggle')) return; // 已初始化

        const topbarRight = document.querySelector('#topBar .topbar-right');
        const searchGroup = document.querySelector('#topBar .search-group');
        const header = document.getElementById('topBar');
        if (!topbarRight || !header) return;

        // 下拉开关按钮（放在顶栏最右侧）
        const toggle = document.createElement('button');
        toggle.id = 'mobileTopbarToggle';
        toggle.title = t('toolbar.more_tools') || '更多';
        toggle.innerHTML = '<span class="icon icon-menu"></span><span class="dropdown-arrow">&#9662;</span>';
        topbarRight.appendChild(toggle);

        // 下拉菜单容器（顶部带版本信息行，用于确认手机实际加载的前端版本）
        const menu = document.createElement('div');
        menu.id = 'mobileTopbarMenu';
        const verRow = document.createElement('div');
        verRow.className = 'mobile-topbar-menu-version';
        verRow.textContent = '前端版本 ' + ASSET_VER;
        menu.appendChild(verRow);
        menu.className = 'mobile-topbar-menu';
        menu.style.display = 'none';

        // 搬移的控件（DOM 移动保留事件绑定）
        const movers = [
            searchGroup ? searchGroup.querySelector('#advancedSearchToggle') : null,
            searchGroup ? searchGroup.querySelector('#paramTagsToggle') : null,
            document.getElementById('btnThumbSettings'),
            document.getElementById('btnRefresh'),
            document.getElementById('btnToggleTheme'),
        ];
        movers.forEach(el => { if (el) menu.appendChild(el); });
        header.appendChild(menu);

        // 纯图标按钮在菜单里补文字标签
        const menuLabels = {
            btnToggleTheme: 'settings.theme',
        };
        Object.entries(menuLabels).forEach(([id, key]) => {
            const btn = document.getElementById(id);
            if (!btn || btn.querySelector('.menu-label')) return;
            const span = document.createElement('span');
            span.className = 'menu-label';
            span.setAttribute('data-i18n', key);
            const translated = t(key);
            span.textContent = translated !== key ? translated : (btn.title || key);
            btn.appendChild(span);
        });

        const close = () => { menu.style.display = 'none'; toggle.classList.remove('active'); };
        toggle.addEventListener('click', (e) => {
            e.stopPropagation();
            if (menu.style.display === 'none') {
                menu.style.display = 'flex';
                toggle.classList.add('active');
            } else {
                close();
            }
        });
        // 点击菜单外区域关闭
        document.addEventListener('click', (e) => {
            if (menu.style.display !== 'none' && !menu.contains(e.target) && !toggle.contains(e.target)) close();
        });
    }
    window.__initMobileTopbar = initMobileTopbar;

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
        applyAccentColor,
        resetAccentColor,
        applyTheme,
        applyThemeById,
        getThemeInfo
    };
})();

// ==================== 启动应用 ====================

document.addEventListener('DOMContentLoaded', () => {
    App.init();
});

// ★ 启动打点：window load 事件（Wails 在页面加载完成后才显示窗口，近似"界面出现"时刻）
window.addEventListener('load', () => {
    if (window.go && window.go.main && window.go.main.App && window.go.main.App.LogStartupTiming) {
        window.go.main.App.LogStartupTiming('window-load');
    }
});
