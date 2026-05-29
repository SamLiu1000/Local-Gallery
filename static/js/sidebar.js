/* ============================================================
   sidebar.js - 左侧面板管理（v8 - 原生文件夹对话框 + 移除手动输入）
      文件夹树（基于本地导入文件生成）、标签树、API 配置面板
      核心修复：
      1. 展开状态持久化到后端 settings，刷新后恢复
      2. 文件夹排序固定，不随后端返回顺序变化
      3. 编辑模式：可上下拖动文件夹顺序，退出编辑后固定
      4. Wails 环境使用原生系统对话框选择文件夹，无需手动输入路径
      ============================================================ */

// ==================== 共享标签样式工具（window.TagStyle） ====================

const TagStyle = {
    /** 将标签样式属性应用到 DOM 元素上 */
    apply(el, tag) {
        const color = tag.color || '#9b59b6';
        const fillStyle = tag.fillStyle || 'solid';
        const fillOpacity = typeof tag.fillOpacity === 'number' ? tag.fillOpacity : 100;
        const shape = tag.shape || 'pill';
        const size = tag.size || 'md';
        const textSize = typeof tag.textSize === 'number' ? tag.textSize : null;
        const weight = tag.weight || 'regular';
        const borderStyle = tag.borderStyle || 'solid';
        const borderOpacity = typeof tag.borderOpacity === 'number' ? tag.borderOpacity : 100;
        const icon = tag.icon || '';

        el.classList.remove('tag-fill-solid', 'tag-fill-light', 'tag-fill-dark', 'tag-fill-outline');
        el.classList.remove('tag-shape-pill', 'tag-shape-square', 'tag-shape-sharp', 'tag-shape-soft', 'tag-shape-round', 'tag-shape-leftbar', 'tag-shape-avatar');
        el.classList.remove('tag-size-sm', 'tag-size-md', 'tag-size-lg');
        el.classList.remove('tag-weight-regular', 'tag-weight-bold');
        el.classList.remove('tag-border-dashed', 'tag-border-none');

        // Avatar tags: minimal styling, custom rendering handled by caller
        if (tag.tagType === 'avatar' || tag.tagType === 'html') {
            el.classList.add('tag-shape-avatar');
            el.style.background = 'transparent';
            el.style.color = '';
            el.style.border = 'none';
            el.style.borderRadius = '';
            el.style.padding = '';
            return;
        }

        el.classList.add('tag-fill-' + fillStyle);
        el.classList.add('tag-shape-' + shape);
        el.classList.add('tag-size-' + size);
        el.classList.add('tag-weight-' + weight);
        if (borderStyle === 'dashed') {
            el.classList.add('tag-border-dashed');
        } else if (borderStyle === 'none') {
            el.classList.add('tag-border-none');
        }

        const bgColor = tag.bgColor;
        const textColor = tag.textColor;
        const opacity = fillOpacity / 100;

        if (bgColor || textColor) {
            // Custom colors: use provided values, fall back to computed ones
            if (bgColor) el.style.background = TagStyle.alphaColor(bgColor, opacity);
            else el.style.background = TagStyle.alphaColor(color, opacity);
            if (textColor) el.style.color = textColor;
            else el.style.color = '#fff';
            const borderOpacityRatio = borderOpacity / 100;
            el.style.border = borderStyle === 'dashed'
                ? '1.5px dashed ' + TagStyle.hexToRgba(color, borderOpacityRatio)
                : borderStyle === 'none' ? 'none' : '1.5px solid ' + TagStyle.hexToRgba(color, borderOpacityRatio);
            // 应用自定义字体大小
            if (textSize) {
                el.style.fontSize = textSize + 'px';
            }
            return;
        }

        switch (fillStyle) {
            case 'solid':
            case 'dark':
            case 'light':
                el.style.background = TagStyle.alphaColor(color, opacity);
                el.style.color = TagStyle.isLightColor(color) ? '#1a1a2e' : '#fff';
                const borderOpacityRatio = borderOpacity / 100;
                el.style.border = borderStyle === 'dashed'
                    ? '1.5px dashed ' + TagStyle.hexToRgba(color, borderOpacityRatio)
                    : borderStyle === 'none' ? 'none' : '1.5px solid ' + TagStyle.hexToRgba(color, borderOpacityRatio);
                break;
            case 'outline':
                el.style.background = 'transparent';
                el.style.color = color;
                el.style.border = borderStyle === 'dashed'
                    ? '1.5px dashed ' + color
                    : borderStyle === 'none' ? 'none' : '1.5px solid ' + color;
                break;
        }

        if (shape === 'leftbar') {
            el.style.borderLeft = '3px solid ' + color;
            el.style.borderTop = 'none';
            el.style.borderRight = 'none';
            el.style.borderBottom = 'none';
            el.style.background = 'transparent';
            el.style.borderRadius = '0 4px 4px 0';
            el.style.paddingLeft = '8px';
        }

        // 应用自定义字体大小
        if (textSize) {
            el.style.fontSize = textSize + 'px';
        }
    },

    hexToRgba(hex, alpha) {
        const r = parseInt(hex.slice(1, 3), 16);
        const g = parseInt(hex.slice(3, 5), 16);
        const b = parseInt(hex.slice(5, 7), 16);
        return `rgba(${r},${g},${b},${alpha})`;
    },

    alphaColor(color, opacity) {
        if (opacity >= 1) return color;
        if (color.startsWith('rgba(')) {
            return color.replace(/[\d.]+\)$/, (opacity * parseFloat(color.match(/[\d.]+\)$/)[0])).toFixed(2) + ')');
        }
        if (color.startsWith('rgb(')) {
            return color.replace('rgb(', 'rgba(').replace(')', ', ' + opacity.toFixed(2) + ')');
        }
        if (color.startsWith('#')) {
            const r = parseInt(color.slice(1, 3), 16);
            const g = parseInt(color.slice(3, 5), 16);
            const b = parseInt(color.slice(5, 7), 16);
            return 'rgba(' + r + ',' + g + ',' + b + ',' + opacity.toFixed(2) + ')';
        }
        return color;
    },

    darkenColor(hex, amount) {
        let r = parseInt(hex.slice(1, 3), 16);
        let g = parseInt(hex.slice(3, 5), 16);
        let b = parseInt(hex.slice(5, 7), 16);
        r = Math.max(0, Math.floor(r * (1 - amount)));
        g = Math.max(0, Math.floor(g * (1 - amount)));
        b = Math.max(0, Math.floor(b * (1 - amount)));
        return '#' + [r, g, b].map(c => c.toString(16).padStart(2, '0')).join('');
    },

    isLightColor(hex) {
        const r = parseInt(hex.slice(1, 3), 16);
        const g = parseInt(hex.slice(3, 5), 16);
        const b = parseInt(hex.slice(5, 7), 16);
        return (r * 299 + g * 587 + b * 114) / 1000 > 150;
    }
};

const Sidebar = (() => {
    const t = (typeof I18n !== "undefined" ? I18n.t : (s) => s);
    // DOM
    let panelTabs, panelFolder, panelTag, panelApi;
    let folderTree, tagTree;
    let btnSelectFolder, btnRemoveFolder, hiddenFolderInput;
    let btnNewTag, btnEditTags, btnTagColumns, filterFavorites;
    let apiConfigSelect, apiConfigForm;
    let btnNewApiConfig, btnCloneApiConfig, btnRenameApiConfig, btnDeleteApiConfig, btnSaveApiConfig, btnTestApiConfig;
    let batchActions, batchCount, leftPanel, toggleLeftPanelBtn;

    // 状态
    let currentMode = 'folder';
    let folderRoots = [];        // [{ name, path, displayPath, children, expanded, imageCount, thumbCount, isRoot }]
    let tags = [];
    let apiConfigs = [];
    let cachedModelList = [];    // 已检测到的模型列表缓存
    let currentApiConfigId = null;
    let isEditingFolderOrder = false; // 文件夹排序编辑模式

    // 回调
    let onFolderSelected = null;
    let onTagSelected = null;
    let onFilterFavorites = null;
    let onBatchAction = null;

    // 当前正在加载的文件夹路径（用于在节点上显示进度）
    let loadingFolderPath = null;
    // 当前选中的文件夹路径（用于恢复 active 状态和进度条高亮）
    let activeFolderPath = null;

    // 文件夹拖拽状态
    let folderDrag = null;

    // 内联重命名状态
    let inlineEditInput = null;
    let inlineEditNode = null;

    // ★ 修复：refreshFolderTree 全局锁，防止并发调用导致 DOM 竞态覆写
    let isFolderTreeRefreshing = false;

    // ★ 展开状态：单一真相来源（内存 Map），启动时一次性加载，之后不走存储
    let expandedStateCache = null;      // Map<path, boolean>
    let saveExpandedTimer = null;       // 防抖持久化定时器
    let currentRefreshId = 0;          // 请求序号，解决竞态

    // ★ 性能修复：懒加载标记，避免每次切换标签都重新请求数据
    let folderTreeLoaded = false;
    let tagTreeLoaded = false;
    let lastFolderTreeHash = '';

    // ==================== 初始化 ====================

    function init(callbacks) {
        initFolderContextMenu();
        panelTabs = document.querySelectorAll('.panel-tab');
        panelFolder = document.getElementById('panelFolder');
        panelTag = document.getElementById('panelTag');
        panelApi = document.getElementById('panelApi');
        folderTree = document.getElementById('folderTree');
        tagTree = document.getElementById('tagTree');
        btnSelectFolder = document.getElementById('btnSelectFolder');
        btnRemoveFolder = document.getElementById('btnRemoveFolder');
        hiddenFolderInput = document.getElementById('hiddenFolderInput');
        btnNewTag = document.getElementById('btnNewTag');
        btnEditTags = document.getElementById('btnEditTags');
        btnTagColumns = document.getElementById('btnTagColumns');
        filterFavorites = document.getElementById('filterFavorites');
        apiConfigSelect = document.getElementById('apiConfigSelect');
        apiConfigForm = document.getElementById('apiConfigForm');
        btnNewApiConfig = document.getElementById('btnNewApiConfig');
        btnCloneApiConfig = document.getElementById('btnCloneApiConfig');
        btnRenameApiConfig = document.getElementById('btnRenameApiConfig');
        btnDeleteApiConfig = document.getElementById('btnDeleteApiConfig');
        btnSaveApiConfig = document.getElementById('btnSaveApiConfig');
        btnTestApiConfig = document.getElementById('btnTestApiConfig');
        batchActions = document.getElementById('batchActions');
        batchCount = document.getElementById('batchCount');
        leftPanel = document.getElementById('leftPanel');
        toggleLeftPanelBtn = document.getElementById('toggleLeftPanel');

        if (callbacks) {
            onFolderSelected = callbacks.onFolderSelected;
            onTagSelected = callbacks.onTagSelected;
            onFilterFavorites = callbacks.onFilterFavorites;
            onBatchAction = callbacks.onBatchAction;
        }

        bindEvents();
        initModelDropdown();

        // 恢复左侧面板展开状态（移动端默认关闭面板）
        const isMobile = document.body.classList.contains('mobile');
        const savedLeftPanelCollapsed = isMobile ? 'true' : localStorage.getItem('leftPanelCollapsed');
        if (savedLeftPanelCollapsed === 'true') {
            if (!isMobile) {
                leftPanel.classList.add('collapsed');
                document.documentElement.style.setProperty('--left-panel-width', '0px');
            }
            if (toggleLeftPanelBtn) toggleLeftPanelBtn.classList.add('active');
        }

        // ★ 事件委托：在 folderTree 容器上统一处理点击，不受 refreshFolderTree 重建 DOM 影响
        folderTree.addEventListener('click', (e) => {
            if (isEditingFolderOrder) return;

            // 展开/折叠按钮
            const toggle = e.target.closest('.tree-toggle');
            if (toggle) {
                e.stopPropagation();
                const container = toggle.closest('.tree-node');
                const node = container && container._nodeData;
                if (node) toggleExpand(node, container);
                return;
            }

            // 文件夹头部 → 选中文件夹
            const header = e.target.closest('.tree-node-header');
            if (header) {
                console.log('[Sidebar] 点击文件夹:', header.dataset.path);
                const prevActivePath = activeFolderPath;
                folderTree.querySelectorAll('.tree-node-header.active').forEach(el => el.classList.remove('active'));
                header.classList.add('active');
                activeFolderPath = header.dataset.path;

                const container = header.closest('.tree-node');
                const node = container && container._nodeData;
                if (node) {
                    if (typeof Gallery !== 'undefined' && Gallery.filterByFolder) {
                        if (Gallery._beginScanLoad) Gallery._beginScanLoad(node.path);
                        Gallery.filterByFolder(node.path, node.displayName || node.name);
                    }
                    if (onFolderSelected) onFolderSelected(node);
                }
            }
        });

        // ★ 事件委托：右键菜单
        folderTree.addEventListener('contextmenu', (e) => {
            const header = e.target.closest('.tree-node-header');
            if (header) {
                e.preventDefault();
                e.stopPropagation();
                const container = header.closest('.tree-node');
                const node = container && container._nodeData;
                if (node) showFolderContextMenu(node, e.clientX, e.clientY);
            }
        });

        // 监听缩略图进度事件，自动刷新导航栏计数（节流：最多每 200ms 刷一次，避免高频 DOM 重建闪动）
        let _pendingTreeRefresh = null;
        try {
            if (window.runtime && window.runtime.EventsOn) {
                window.runtime.EventsOn('thumb:progress', () => {
                    if (!folderTreeLoaded) return;
                    if (_pendingTreeRefresh) return; // 已有待执行的刷新，合并
                    _pendingTreeRefresh = setTimeout(() => {
                        _pendingTreeRefresh = null;
                        refreshFolderTree();
                    }, 200);
                });
            }
        } catch (e) { /* ignore */ }

        // 从服务器同步标签色板（服务器优先 → localStorage 回退）
        syncTagColorPresets();
    }

    async function syncTagColorPresets() {
        try {
            if (typeof Storage !== 'undefined' && Storage.getSetting) {
                const serverPresets = await Storage.getSetting('tagColorPresets', null);
                if (serverPresets && Array.isArray(serverPresets) && serverPresets.length > 0) {
                    localStorage.setItem('tagColorPresets', JSON.stringify(serverPresets));
                } else {
                    const localPresets = localStorage.getItem('tagColorPresets');
                    if (localPresets && Storage.setSetting) {
                        await Storage.setSetting('tagColorPresets', JSON.parse(localPresets));
                    }
                }
            }
        } catch (e) { /* 静默 */ }
    }

    function saveTagColorPresetsToServer(colorPresets) {
        try { localStorage.setItem('tagColorPresets', JSON.stringify(colorPresets)); } catch (e) {}
        if (typeof Storage !== 'undefined' && Storage.setSetting) {
            Storage.setSetting('tagColorPresets', colorPresets);
        }
    }

    function bindEvents() {
        panelTabs.forEach(tab => {
            tab.addEventListener('click', () => switchTab(tab.dataset.tab));
        });

        btnSelectFolder.addEventListener('click', () => {
            openSystemFolderPicker();
        });

        if (hiddenFolderInput) {
            hiddenFolderInput.addEventListener('change', async (event) => {
                await handleFolderInputChange(event);
            });
        }

        if (btnRemoveFolder) {
            btnRemoveFolder.addEventListener('click', () => {
                const activeHeader = folderTree.querySelector('.tree-node-header.active');
                if (activeHeader) {
                    const path = activeHeader.dataset.path;
                    if (path) {
                        removeFolderPath(path);
                    }
                } else {
                    App.showToast(t('toast.folder_select_to_remove'), 'warning');
                }
            });
        }

        // 添加编辑排序按钮事件
        const btnEditFolderOrder = document.getElementById('btnEditFolderOrder');
        if (btnEditFolderOrder) {
            btnEditFolderOrder.addEventListener('click', toggleEditFolderOrder);
        }

        const btnCollapseAll = document.getElementById('btnCollapseAllFolders');
        if (btnCollapseAll) {
            btnCollapseAll.addEventListener('click', collapseAllFolders);
        }

        btnNewTag.addEventListener('click', () => showNewTagDialog());
        btnEditTags.addEventListener('click', () => toggleEditTags());
        if (btnTagColumns) {
            window.tagDropdownColumnMode = window.tagDropdownColumnMode || 1;
            btnTagColumns.innerHTML = window.tagDropdownColumnMode === 1
                ? '<span class="icon icon-columns"></span><span>' + t('sidebar.double_column') + '</span>'
                : '<span class="icon icon-menu"></span><span>' + t('sidebar.single_column') + '</span>';
            btnTagColumns.addEventListener('click', () => {
                window.tagDropdownColumnMode = window.tagDropdownColumnMode === 1 ? 2 : 1;
                const textSpan = btnTagColumns.querySelector('span:last-child');
                if (textSpan) {
                    textSpan.textContent = window.tagDropdownColumnMode === 1 ? t('sidebar.double_column') : t('sidebar.single_column');
                }
                btnTagColumns.title = window.tagDropdownColumnMode === 1 ? t('sidebar.switch_to_single') : t('sidebar.switch_to_double');
                renderTagTree();
            });
        }
        filterFavorites.addEventListener('change', () => {
            if (onFilterFavorites) onFilterFavorites(filterFavorites.checked);
        });

        btnNewApiConfig.addEventListener('click', newApiConfig);
        btnDeleteApiConfig.addEventListener('click', deleteCurrentApiConfig);
        btnCloneApiConfig.addEventListener('click', cloneCurrentApiConfig);
        btnRenameApiConfig.addEventListener('click', renameCurrentApiConfig);
        btnSaveApiConfig.addEventListener('click', saveApiConfig);
        btnTestApiConfig.addEventListener('click', testApiConfig);
        apiConfigSelect.addEventListener('change', onApiConfigSelectChange);

        // 左侧面板收缩按钮
        if (toggleLeftPanelBtn) {
            toggleLeftPanelBtn.addEventListener('click', toggleLeftPanel);
        }

        document.getElementById('apiTemperature').addEventListener('input', (e) => {
            document.getElementById('tempValue').textContent = e.target.value;
        });

        // 反推日志按钮
        const btnOpenReverseLog = document.getElementById('btnOpenReverseLog');
        if (btnOpenReverseLog) {
            btnOpenReverseLog.addEventListener('click', async () => {
                if (typeof WailsBridge !== 'undefined' && WailsBridge.openReverseLog) {
                    await WailsBridge.openReverseLog();
                }
            });
        }

        // 批量操作按钮 — 使用事件委托，避免 innerHTML 替换后丢失监听器
        const batchActionMap = {
            'btnBatchTag': 'tag',
            'btnBatchUntag': 'untag',
            'btnBatchFavorite': 'favorite',
            'btnBatchUnfavorite': 'unfavorite',
            'btnBatchReverse': 'reverse',
            'btnClearSelection': 'clear'
        };
        if (batchActions) {
            batchActions.addEventListener('click', (e) => {
                const btn = e.target.closest('button');
                const action = btn ? batchActionMap[btn.id] : undefined;
                if (action && onBatchAction) {
                    onBatchAction(action);
                }
            });
        }
    }

    // ==================== 左侧面板收缩/展开 ====================

    function toggleLeftPanel() {
        if (!leftPanel) return;
        // 移动端走抽屉式逻辑
        if (document.body.classList.contains('mobile')) {
            const overlay = document.getElementById('panelOverlay');
            const isOpen = leftPanel.classList.toggle('mobile-open');
            const btnFolder = document.getElementById('mobileNavFolder');
            if (overlay) overlay.classList.toggle('show', isOpen);
            if (btnFolder) btnFolder.classList.toggle('active', isOpen);
            return;
        }
        const isCollapsed = leftPanel.classList.toggle('collapsed');
        localStorage.setItem('leftPanelCollapsed', String(isCollapsed));
        document.documentElement.style.setProperty('--left-panel-width', isCollapsed ? '0px' : (localStorage.getItem('leftPanelWidth') || '300') + 'px');
        toggleLeftPanelBtn.classList.toggle('active', isCollapsed);
    }

    // ==================== 标签切换 ====================

    function switchTab(tabName) {
        currentMode = tabName;
        panelTabs.forEach(t => t.classList.toggle('active', t.dataset.tab === tabName));
        panelFolder.classList.toggle('active', tabName === 'folder');
        panelTag.classList.toggle('active', tabName === 'tag');
        panelApi.classList.toggle('active', tabName === 'api');

        // ★ 性能修复：仅首次切到标签时加载数据，后续切换走缓存（数据变更操作会主动刷新）
        if (tabName === 'folder' && !folderTreeLoaded) refreshFolderTree();
        if (tabName === 'tag' && !tagTreeLoaded) refreshTagTree();
        if (tabName === 'api') refreshApiConfigSelect();
    }

    // ★ 性能修复：当文件夹/标签数据发生变化时，重置加载标记以便下次切标签时刷新
    // 如果当前就在对应标签下，立即刷新而不是等下次切换
    function invalidateFolderTree() {
        folderTreeLoaded = false;
        lastFolderTreeHash = '';
        if (currentMode === 'folder') refreshFolderTree();
    }
    function invalidateTagTree() {
        tagTreeLoaded = false;
        if (currentMode === 'tag') refreshTagTree();
    }

    // ==================== 展开状态持久化 ====================

    /**
     * 保存文件夹展开状态到后端 settings
     * 使用 Storage.setSetting 持久化
     */
    async function saveExpandedStates() {
        if (!expandedStateCache) return;
        try {
            const expandedMap = {};
            for (const [path, expanded] of expandedStateCache) {
                expandedMap[path] = expanded;
            }

            if (typeof WailsBridge !== 'undefined' && WailsBridge.isWails()) {
                await WailsBridge.setSidebarSetting('sidebar_expanded', JSON.stringify(expandedMap));
            } else if (typeof Storage !== 'undefined' && Storage.setSetting) {
                await Storage.setSetting('sidebar_expanded', expandedMap);
            }
        } catch (err) {
            console.warn('[Sidebar] 保存展开状态失败:', err.message);
        }
    }

    /**
     * 从 SQLite 加载文件夹展开状态
     */
    async function initExpandedStates() {
        if (expandedStateCache !== null) return;
        expandedStateCache = new Map();
        let saved = null;
        try {
            if (typeof WailsBridge !== 'undefined' && WailsBridge.isWails()) {
                const result = await WailsBridge.getSidebarSetting('sidebar_expanded');
                if (result && result.success && result.value) {
                    saved = JSON.parse(result.value);
                }
            }
            if (!saved) {
                // 旧数据回退：从 settings / localStorage 迁移
                try {
                    if (typeof Storage !== 'undefined' && Storage.getSetting) {
                        saved = await Storage.getSetting('sidebar_expanded', null);
                    }
                } catch (e) {}
                if (!saved || Object.keys(saved).length === 0) {
                    try {
                        const raw = localStorage.getItem('sidebar_expanded');
                        if (raw) saved = JSON.parse(raw);
                    } catch (e) {}
                }
                // 迁移旧数据到 SQLite
                if (saved && Object.keys(saved).length > 0 &&
                    typeof WailsBridge !== 'undefined' && WailsBridge.isWails()) {
                    WailsBridge.setSidebarSetting('sidebar_expanded', JSON.stringify(saved));
                }
            }
        } catch (e) {}
        if (saved) {
            for (const [path, expanded] of Object.entries(saved)) {
                expandedStateCache.set(path, expanded);
            }
        }
    }

    // 用内存缓存的展开状态应用到树节点（不读存储，无 IO）
    function applyExpandedStates(treeNodes) {
        function apply(nodes) {
            for (const node of nodes) {
                if (expandedStateCache.has(node.path)) {
                    node.expanded = expandedStateCache.get(node.path);
                } else {
                    // 新文件夹默认折叠，用户手动展开
                    node.expanded = false;
                }
                if (node.children) apply(node.children);
            }
        }
        apply(treeNodes);
    }

    // ==================== 排序持久化 ====================

    /**
     * 保存文件夹排序顺序到 SQLite
     */
    async function saveFolderOrder() {
        try {
            const orderList = folderRoots.map(node => node.path);
            if (typeof WailsBridge !== 'undefined' && WailsBridge.isWails()) {
                await WailsBridge.setSidebarSetting('sidebar_folder_order', JSON.stringify(orderList));
            } else if (typeof Storage !== 'undefined' && Storage.setSetting) {
                await Storage.setSetting('sidebar_folder_order', orderList);
            }
        } catch (err) {
            console.warn('[Sidebar] 保存文件夹排序失败:', err.message);
        }
    }

    /**
     * 从 SQLite 加载文件夹排序顺序
     */
    async function loadFolderOrder(treeNodes) {
        try {
            let orderList = [];
            if (typeof WailsBridge !== 'undefined' && WailsBridge.isWails()) {
                const result = await WailsBridge.getSidebarSetting('sidebar_folder_order');
                if (result && result.success && result.value) {
                    orderList = JSON.parse(result.value);
                }
            }
            if (!Array.isArray(orderList) || orderList.length === 0) {
                // 旧数据回退
                if (typeof Storage !== 'undefined' && Storage.getSetting) {
                    orderList = await Storage.getSetting('sidebar_folder_order', []);
                }
                if (!Array.isArray(orderList)) orderList = [];
            }

            if (!Array.isArray(orderList)) orderList = [];

            // 构建顺序 Map
            const orderMap = new Map();
            orderList.forEach((path, index) => {
                orderMap.set(path, index);
            });

            // 新文件夹自动追加到列表末尾
            let changed = false;
            for (const node of treeNodes) {
                if (!orderMap.has(node.path)) {
                    orderMap.set(node.path, orderList.length);
                    orderList.push(node.path);
                    changed = true;
                }
            }
            if (changed) {
                if (typeof WailsBridge !== 'undefined' && WailsBridge.isWails()) {
                    WailsBridge.setSidebarSetting('sidebar_folder_order', JSON.stringify(orderList));
                } else if (typeof Storage !== 'undefined' && Storage.setSetting) {
                    Storage.setSetting('sidebar_folder_order', orderList);
                }
            }

            treeNodes.sort((a, b) => {
                const ai = orderMap.get(a.path);
                const bi = orderMap.get(b.path);
                // 两个都应该在 orderMap 中（新节点已自动追加）
                return (ai ?? orderList.length) - (bi ?? orderList.length);
            });
        } catch (err) {
            console.warn('[Sidebar] 加载文件夹排序失败:', err.message);
        }
    }

    // ==================== 文件夹树（合并前端 Gallery 和后端数据） ====================

    /**
     * ★ 修复：添加全局锁 isFolderTreeRefreshing，防止并发调用导致 DOM 竞态覆写
     */
    async function refreshFolderTree() {
        if (isFolderTreeRefreshing) return;
        isFolderTreeRefreshing = true;
        const requestId = ++currentRefreshId; // ★ 请求序号，旧请求结果丢弃
        try {
            // ★ 启动时一次性从存储加载到内存，之后只读内存
            await initExpandedStates();

            // 1. 从后端获取文件夹树
            let serverTree = [];
            let retryCount = 0;
            const maxRetries = 3;
            while (retryCount < maxRetries) {
                try {
                    if (typeof WailsBridge !== 'undefined' && WailsBridge.isWails()) {
                        serverTree = await WailsBridge.getFolders();
                    } else {
                        const response = await fetch('/api/folders');
                        if (response.ok) serverTree = await response.json();
                    }
                    // 如果返回有数据的文件夹树，或已经重试多次，则退出循环
                    if (serverTree.length > 0 || retryCount >= maxRetries - 1) break;
                } catch (err) {
                    console.warn('[Sidebar] 从后端加载文件夹树失败:', err.message);
                }
                retryCount++;
                // 等待后端初始化完成后重试
                await new Promise(resolve => setTimeout(resolve, 500));
            }
            if (serverTree.length === 0 && retryCount > 0) {
                console.log('[Sidebar] 多次重试后仍无文件夹数据，可能后端尚未加载完成');
            }

            // ★ 异步 IO 后检查：请求已过期则丢弃
            if (requestId !== currentRefreshId) return;

            // 2. 获取前端导入的文件夹树
            let frontendTree = [];
            if (typeof Gallery !== 'undefined' && Gallery.getFolderTree) {
                frontendTree = Gallery.getFolderTree() || [];
            }

            // 3. 合并
            const mergedTree = mergeFolderTrees(serverTree, frontendTree);

            // 4. displayName
            let importedRootsList = [];
            if (typeof Gallery !== 'undefined' && Gallery.getImportedRoots) {
                importedRootsList = Gallery.getImportedRoots();
            }
            applyDisplayNames(mergedTree, importedRootsList);

            // 5. 排序
            await loadFolderOrder(mergedTree);

            if (requestId !== currentRefreshId) return; // 再次检查

            // ★ 6. 展开状态：只读内存缓存，不读存储
            applyExpandedStates(mergedTree);

            folderRoots = mergedTree;
            renderFolderTree();
            folderTreeLoaded = true;
        } catch (err) {
            console.error('[Sidebar] 刷新文件夹树失败:', err);
        } finally {
            isFolderTreeRefreshing = false;
        }
    }

    /**
     * 合并后端和前端的两棵树
     * 以后端数据为主，补充前端独有的导入
     * 去重：如果后端已有同名根节点，则跳过前端的同名节点
     */
    function mergeFolderTrees(serverTree, frontendTree) {
        const merged = [];
        const seenPaths = new Set();

        // 先添加后端节点
        for (const node of serverTree) {
            const normalizedPath = node.path.replace(/\\/g, '/').toLowerCase();
            if (!seenPaths.has(normalizedPath)) {
                seenPaths.add(normalizedPath);
                // 新文件夹默认折叠，等待用户手动展开
                merged.push({
                    name: node.name,
                    path: node.path,
                    displayPath: node.path,
                    displayName: node.name,
                    expanded: false,
                    imageCount: node.imageCount || 0,
                    thumbCount: node.thumbCount || 0,
                    isRoot: true,
                    children: (node.children || []).map(c => convertChildNode(c))
                });
            }
        }

        // 再补充前端独有的节点
        for (const node of frontendTree) {
            const normalizedPath = node.path.replace(/\\/g, '/').toLowerCase();
            if (!seenPaths.has(normalizedPath)) {
                seenPaths.add(normalizedPath);
                merged.push({
                    name: node.name,
                    path: node.path,
                    displayPath: node.displayPath || node.path,
                    displayName: node.displayName || node.name,
                    expanded: false,
                    imageCount: node.imageCount || 0,
                    thumbCount: node.thumbCount || 0,
                    isRoot: true,
                    children: (node.children || []).map(c => convertChildNode(c))
                });
            }
        }

        return merged;
    }

    function convertChildNode(node) {
        return {
            name: node.name,
            path: node.path,
            displayPath: node.displayPath || node.path,
            expanded: false,
            imageCount: node.imageCount || 0,
            thumbCount: node.thumbCount || 0,
            isRoot: false,
            children: (node.children || []).map(c => convertChildNode(c))
        };
    }

    /**
     * 将 Gallery.importedRoots 中的 displayName 应用到树节点上
     */
    function applyDisplayNames(treeNodes, importedRootsList) {
        if (!importedRootsList || importedRootsList.length === 0) return;

        // ★ Bug 2: 规范化路径再比较，避免斜杠风格不一致导致 Map 查找失败
        const nameMap = new Map();
        for (const root of importedRootsList) {
            nameMap.set((root.rootId || '').replace(/\\/g, '/'), root.displayName || root.name);
        }

        for (const node of treeNodes) {
            const key = (node.path || '').replace(/\\/g, '/');
            if (nameMap.has(key)) {
                node.displayName = nameMap.get(key);
            }
        }
    }

    // ==================== 渲染文件夹树 ====================

    function renderFolderTree() {
        if (!folderTree) return;

        if (folderRoots.length === 0) {
            folderTree.innerHTML = '<p class="placeholder-text">' + t('panel.folder_placeholder') + '</p>';
            lastFolderTreeHash = '';
            return;
        }

        // ★ 性能修复：比较树结构与上次是否一致，一致则跳过 DOM 重建
        const currentHash = JSON.stringify(folderRoots.map(r => ({
            p: r.path, n: r.displayName || r.name, e: r.expanded,
            c: r.imageCount, t: r.thumbCount, ch: r.children
        })));
        if (currentHash === lastFolderTreeHash) return;
        lastFolderTreeHash = currentHash;

        const fragment = document.createDocumentFragment();

        for (let i = 0; i < folderRoots.length; i++) {
            const root = folderRoots[i];
            const rootEl = createFolderNode(root, 0);
            rootEl.dataset.folderIndex = i;
            fragment.appendChild(rootEl);
        }

        folderTree.innerHTML = '';
        folderTree.appendChild(fragment);

        // 恢复 active 状态（renderFolderTree 重建了全部 DOM）
        if (activeFolderPath) {
            const activeHeader = folderTree.querySelector('.tree-node-header[data-path="' + CSS.escape(activeFolderPath) + '"]');
            if (activeHeader) {
                activeHeader.classList.add('active');
            }
        }

        // ★ 修复：DOM 重建后索引图标回到初始状态，需立即刷新为实际状态
        updateIndexStatusUI();

        // Cancel any active folder drag
        if (folderDrag && folderDrag.active) {
            cancelAnimationFrame(folderDrag.raf);
            folderDrag = null;
        }

        // Set up pointer-based drag for folders in edit mode
        if (isEditingFolderOrder) {
            initFolderDrag(folderTree);
        }

        // 更新编辑模式按钮状态
        updateEditOrderButtonUI();
    }

    /**
     * 创建单个文件夹树节点（递归）
     */
    function createFolderNode(node, depth) {
        const container = document.createElement('div');
        container.className = 'tree-node';
        container.dataset.path = node.path;
        container.dataset.isRoot = node.isRoot ? 'true' : 'false';
        container._nodeData = node; // 事件委托时获取节点数据

        // 头部
        const header = document.createElement('div');
        header.className = 'tree-node-header';
        if (node.isRoot) {
            header.classList.add('root-header');
        }
        header.dataset.path = node.path;

        // 展开/折叠按钮
        const toggle = document.createElement('span');
        toggle.className = 'tree-toggle';
        if (node.expanded && node.children && node.children.length > 0) {
            toggle.classList.add('expanded');
        }
        toggle.textContent = '▶';
        if (!node.children || node.children.length === 0) {
            toggle.style.visibility = 'hidden';
        }

        // 图标
        const icon = document.createElement('span');
        icon.className = 'tree-icon';
        icon.innerHTML = '<span class="icon icon-folder"></span>';

        // 标签
        const label = document.createElement('span');
        label.className = 'tree-label';
        label.textContent = node.displayName || node.name;

        // 图片计数 + 缩略图进度条
        const total = node.imageCount || 0;
        const thumbCount = node.thumbCount || 0;
        const count = document.createElement('span');
        count.className = 'tree-count';
        count.textContent = total;
        count.dataset.total = total;
        count.dataset.thumbCount = thumbCount;
        if (total > 0 && thumbCount > 0) {
            const pct = Math.round((thumbCount / total) * 100);
            count.style.background = 'linear-gradient(to right, var(--thumb-fill, var(--bg-tertiary)) ' + pct + '%, var(--count-bg, var(--bg-input)) ' + pct + '%)';
            count.title = t('sidebar.image_count_tooltip').replace('{total}', total).replace('{thumb}', thumbCount).replace('{pct}', pct);
        } else if (total > 0) {
            count.title = t('sidebar.image_count_no_thumb').replace('{total}', total);
        } else {
            count.title = t('sidebar.image_count_zero');
        }

        // 编辑模式下的排序按钮组（上下箭头 + 拖拽手柄）
        const sortActions = document.createElement('span');
        sortActions.className = 'tree-sort-actions';
        if (!isEditingFolderOrder || !node.isRoot) {
            sortActions.style.display = 'none';
        }

        // 上移按钮
        const btnMoveUp = document.createElement('button');
        btnMoveUp.className = 'tree-sort-btn';
        btnMoveUp.textContent = '▲';
        btnMoveUp.title = t('sidebar.move_up');
        btnMoveUp.addEventListener('click', (e) => {
            e.stopPropagation();
            e.preventDefault();
            moveFolderNode(node.path, -1);
        });

        // 下移按钮
        const btnMoveDown = document.createElement('button');
        btnMoveDown.className = 'tree-sort-btn';
        btnMoveDown.textContent = '▼';
        btnMoveDown.title = t('sidebar.move_down');
        btnMoveDown.addEventListener('click', (e) => {
            e.stopPropagation();
            e.preventDefault();
            moveFolderNode(node.path, 1);
        });

        // 拖拽手柄
        const dragHandle = document.createElement('span');
        dragHandle.className = 'tree-drag-handle';
        dragHandle.textContent = '⠿';
        dragHandle.title = t('sidebar.drag_sort');

        sortActions.appendChild(btnMoveUp);
        sortActions.appendChild(btnMoveDown);
        sortActions.appendChild(dragHandle);

        // 重命名按钮（仅根节点显示，hover 时可见）
        const renameBtn = document.createElement('button');
        renameBtn.className = 'tree-rename-btn';
        renameBtn.innerHTML = '<span class="icon icon-edit"></span>';
        renameBtn.title = t('sidebar.rename_folder_hint');
        if (!node.isRoot) {
            renameBtn.style.display = 'none';
        }
        renameBtn.addEventListener('click', (e) => {
            e.stopPropagation();
            e.preventDefault();
            startInlineRename(node, label, renameBtn);
        });

        header.appendChild(toggle);
        header.appendChild(icon);
        header.appendChild(label);
        header.appendChild(count);

        // ★ 搜索索引按钮（仅根节点）
        if (node.isRoot) {
            const idxBtn = document.createElement('button');
            idxBtn.className = 'tree-index-btn';
            idxBtn.title = t('sidebar.index');
            idxBtn.dataset.rootPath = node.path;
            idxBtn.innerHTML = '<span class="index-icon icon icon-index"></span>';
            idxBtn.addEventListener('click', (e) => {
                e.stopPropagation();
                e.preventDefault();
                toggleIndexRoot(node.path, idxBtn);
            });
            header.appendChild(idxBtn);
        }

        header.appendChild(renameBtn);
        header.appendChild(sortActions);
        container.appendChild(header);

        // 子节点容器
        const childrenContainer = document.createElement('div');
        childrenContainer.className = 'tree-children';
        if (!node.expanded) {
            childrenContainer.style.display = 'none';
        }

        if (node.children && node.children.length > 0) {
            for (const child of node.children) {
                const childEl = createFolderNode(child, depth + 1);
                childrenContainer.appendChild(childEl);
            }
        }

        container.appendChild(childrenContainer);

        // 事件由 folderTree 上的委托处理（不受 DOM 重建影响）
        // Pointer-based drag is set up in initFolderDrag() called from renderFolderTree()

        return container;
    }

    /**
     * 移动文件夹节点（上移/下移）
     * @param {string} path - 节点路径
     * @param {number} direction - 移动方向：-1 上移，1 下移
     */
    function moveFolderNode(path, direction) {
        const index = folderRoots.findIndex(n => n.path === path);
        if (index === -1) return;

        const newIndex = index + direction;
        if (newIndex < 0 || newIndex >= folderRoots.length) return;

        // 交换位置
        const [removed] = folderRoots.splice(index, 1);
        folderRoots.splice(newIndex, 0, removed);

        renderFolderTree();
        saveFolderOrder();
    }

    /**
     * 切换展开/折叠状态
     */
    // ==================== 文件夹右键菜单 ====================

    function showFolderContextMenu(node, x, y) {
        const menu = document.getElementById('folderContextMenu');
        if (!menu) return;
        menu._currentNode = node;
        menu.style.display = 'block';
        menu.style.left = x + 'px';
        menu.style.top = y + 'px';
        requestAnimationFrame(() => {
            const rect = menu.getBoundingClientRect();
            if (rect.right > window.innerWidth) menu.style.left = (x - rect.width) + 'px';
            if (rect.bottom > window.innerHeight) menu.style.top = (y - rect.height) + 'px';
        });
    }

    function initFolderContextMenu() {
        const menu = document.getElementById('folderContextMenu');
        if (!menu || menu._initialized) return;
        menu._initialized = true;

        menu.addEventListener('click', async (e) => {
            const btn = e.target.closest('button');
            if (!btn) return;
            const action = btn.dataset.action;
            const node = menu._currentNode;
            hideFolderContextMenu();
            try {
                if (action === 'refresh') {
                    await refreshSingleFolder(node);
                } else if (action === 'convertTag') {
                    await convertFolderToTag(node);
                }
            } catch (err) {
                console.error('[Sidebar] 菜单操作失败:', err);
                App.showToast(t('toast.op_failed') + ': ' + (err.message || err), 'error');
            }
        });

        document.addEventListener('click', (e) => {
            if (!menu.contains(e.target)) hideFolderContextMenu();
        });
    }

    function hideFolderContextMenu() {
        const menu = document.getElementById('folderContextMenu');
        if (menu) { menu.style.display = 'none'; menu._currentNode = null; }
    }

    async function refreshSingleFolder(node) {
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
            const result = await WailsBridge.rescanFolder(rootPath);
            App.showToast(result.message || '文件夹已刷新', 'success');
        }
        if (typeof Gallery !== 'undefined' && Gallery.refreshRootFromServer) {
            await Gallery.refreshRootFromServer(rootPath);
        }
        await refreshFolderTree();
        if (typeof Gallery !== 'undefined') {
            const currentFolder = Gallery.getCurrentFolder ? Gallery.getCurrentFolder() : null;
            if (currentFolder && currentFolder.path) {
                const currentNorm = currentFolder.path.replace(/\\/g, '/');
                if (currentNorm === normalizedPath || currentNorm.startsWith(normalizedPath + '/') || normalizedPath.startsWith(currentNorm + '/')) {
                    if (Gallery.render) Gallery.render();
                }
            }
        }
    }

    async function convertFolderToTag(node) {
        const folderPath = node.path;
        const folderName = node.displayName || node.name;
        const allTags = await Storage.getAllTags();
        if (allTags.some(t => t.linkedFolder === folderPath)) {
            App.showToast(t('toast.folder_converted'), 'info');
            return;
        }
        const tag = await Storage.addTag({
            name: folderName,
            parentId: null,
            linkedFolder: folderPath,
            color: TAG_COLORS[Math.floor(Math.random() * TAG_COLORS.length)]
        });
        await refreshTagTree();
        App.showToast(t('sidebar.tag_created_from_folder') + ': ' + folderName, 'success');
    }

    function toggleExpand(node, container) {
        node.expanded = !node.expanded;
        // ★ 写内存缓存（即时生效，无 IO）
        expandedStateCache.set(node.path, node.expanded);

        const toggle = container.querySelector('.tree-toggle');
        const children = container.querySelector('.tree-children');

        if (toggle) {
            toggle.classList.toggle('expanded', node.expanded);
        }
        if (children) {
            children.style.display = node.expanded ? 'block' : 'none';
        }

        // ★ 防抖持久化（不阻塞 UI）
        clearTimeout(saveExpandedTimer);
        saveExpandedTimer = setTimeout(() => saveExpandedStates(), 500);
    }

    // ★ 索引状态轮询
    let indexStatusPollTimer = null;
    let indexStatusPollAbort = null;

    async function updateIndexStatusUI() {
        if (typeof WailsBridge === 'undefined') return;
        try {
            const statusList = await WailsBridge.getFolderIndexStatus();
            for (const info of statusList) {
                const rootPath = info.rootPath;
                const btn = document.querySelector(`.tree-index-btn[data-root-path="${CSS.escape(rootPath)}"]`);
                if (!btn) continue;
                const icon = btn.querySelector('.index-icon');
                if (!icon) continue;
                if (info.indexing) {
                    icon.className = 'index-icon icon icon-indexing';
                    btn.title = t('sidebar.indexing_progress', { indexed: info.indexed, total: info.total });
                    btn.classList.add('indexing');
                    btn.classList.remove('done');
                } else if (info.done) {
                    icon.className = 'index-icon icon icon-index-done';
                    btn.title = t('sidebar.indexed_count', { n: info.total });
                    btn.classList.add('done');
                    btn.classList.remove('indexing');
                } else {
                    icon.className = 'index-icon icon icon-index';
                    btn.title = info.total > 0 ? t('sidebar.click_to_index', { n: info.total }) : t('sidebar.no_images');
                    btn.classList.remove('done', 'indexing');
                }
            }
        } catch (e) {
            // 忽略
        }
    }

    function startIndexStatusPoll() {
        stopIndexStatusPoll();
        updateIndexStatusUI();
        indexStatusPollTimer = setInterval(updateIndexStatusUI, 2000);
    }

    function stopIndexStatusPoll() {
        if (indexStatusPollTimer) {
            clearInterval(indexStatusPollTimer);
            indexStatusPollTimer = null;
        }
    }

    async function toggleIndexRoot(rootPath, btn) {
        if (typeof WailsBridge === 'undefined') return;
        try {
            const statusList = await WailsBridge.getFolderIndexStatus();
            const info = statusList.find(s => s.rootPath === rootPath);
            if (info && info.indexing) {
                await WailsBridge.stopIndexRoot(rootPath);
                if (btn) {
                    const icon = btn.querySelector('.index-icon');
                    if (icon) icon.className = 'index-icon icon icon-index';
                    btn.classList.remove('indexing', 'done');
                }
            } else {
                await WailsBridge.indexRoot(rootPath);
                if (btn) {
                    const icon = btn.querySelector('.index-icon');
                    if (icon) icon.className = 'index-icon icon icon-indexing';
                    btn.classList.add('indexing');
                    btn.classList.remove('done');
                }
                startIndexStatusPoll();
            }
        } catch (e) {
            console.warn('[Sidebar] 索引操作失败:', e.message);
        }
    }

    function collapseAllFolders() {
        function collapseRecursive(nodes) {
            for (const node of nodes) {
                node.expanded = false;
                expandedStateCache.set(node.path, false);
                if (node.children && node.children.length > 0) {
                    collapseRecursive(node.children);
                }
            }
        }
        collapseRecursive(folderRoots);
        lastFolderTreeHash = ''; // 清除缓存，强制 DOM 重建
        renderFolderTree();
        saveExpandedStates();
    }

    // ==================== 虚拟重命名 ====================

    /**
     * 启动内联重命名：将标签替换为输入框
     * @param {Object} node - 树节点数据
     * @param {HTMLElement} label - 标签元素
     * @param {HTMLElement} renameBtn - 重命名按钮
     */
    function startInlineRename(node, label, renameBtn) {
        // 如果已有正在编辑的输入框，先取消
        if (inlineEditInput) {
            cancelInlineRename();
        }

        const currentName = node.displayName || node.name;

        // 创建输入框
        const input = document.createElement('input');
        input.type = 'text';
        input.className = 'tree-rename-input';
        input.value = currentName;
        input.style.flex = '1';
        input.style.minWidth = '0';

        // 替换标签
        label.style.display = 'none';
        label.parentNode.insertBefore(input, label.nextSibling);

        inlineEditInput = input;
        inlineEditNode = node;

        // 选中全部文字
        input.focus();
        input.select();

        // 回车确认
        input.addEventListener('keydown', async (e) => {
            if (e.key === 'Enter') {
                e.preventDefault();
                await confirmInlineRename(node, input, label);
            } else if (e.key === 'Escape') {
                e.preventDefault();
                cancelInlineRename(label);
            }
        });

        // ★ 修复问题三：使用全局 mousedown 监听替代 blur 的 150ms 延迟，
        //     避免竞态窗口导致 DOM 元素被销毁后仍写入
        const mousedownHandler = (e) => {
            if (inlineEditInput === input && !input.contains(e.target)) {
                document.removeEventListener('mousedown', mousedownHandler);
                confirmInlineRename(node, input, label);
            }
        };
        // 延迟绑定，避免当前点击事件立即触发
        setTimeout(() => {
            document.addEventListener('mousedown', mousedownHandler);
        }, 0);

        // 失去焦点时也尝试确认（作为兜底，处理 Tab 键切换等非点击失焦场景）
        input.addEventListener('blur', async () => {
            // 延迟检查，因为可能是在点击其他元素
            setTimeout(async () => {
                if (inlineEditInput === input) {
                    document.removeEventListener('mousedown', mousedownHandler);
                    await confirmInlineRename(node, input, label);
                }
            }, 150);
        });
    }

    /**
     * 确认内联重命名
     */
    async function confirmInlineRename(node, input, label) {
        // ★ 修复问题三：防重入检查，避免 blur 和 mousedown 同时触发
        if (inlineEditInput !== input) {
            return;
        }

        const newName = input.value.trim();

        // 移除输入框
        if (input.parentNode) {
            input.parentNode.removeChild(input);
        }
        inlineEditInput = null;
        inlineEditNode = null;

        // 恢复标签显示
        label.style.display = '';

        if (!newName || newName === (node.displayName || node.name)) {
            // 名称未改变或为空，不更新
            if (!newName) {
                App.showToast(t('toast.folder_name_empty'), 'warning');
            }
            return;
        }

        // 更新节点数据
        node.displayName = newName;

        // 更新标签文字
        label.textContent = newName;

        // ★ 修复问题一&二：await 调用 Gallery.renameImportedRoot，确保持久化完成后再刷新树
        if (typeof Gallery !== 'undefined' && Gallery.renameImportedRoot) {
            const success = await Gallery.renameImportedRoot(node.path, newName);
            if (success) {
                // ★ 核心修复：等持久化完成后，再刷新树，避免读到旧数据覆盖 displayName
                await refreshFolderTree();
                App.showToast(t('sidebar.folder_renamed_to', { name: newName }), 'success');
            } else {
                // 回滚显示
                node.displayName = node.name;
                label.textContent = node.name;
                App.showToast(t('sidebar.rename_not_found'), 'error');
            }
        } else {
            // Gallery 不可用，只更新本地显示
            App.showToast(t('sidebar.renamed_may_lose', { name: newName }), 'warning');
        }
    }

    /**
     * 取消内联重命名
     */
    function cancelInlineRename(label) {
        if (inlineEditInput) {
            if (inlineEditInput.parentNode) {
                inlineEditInput.parentNode.removeChild(inlineEditInput);
            }
            inlineEditInput = null;
            inlineEditNode = null;
        }
        if (label) {
            label.style.display = '';
        }
    }

    // ==================== 编辑排序模式 ====================

    /**
     * 切换文件夹排序编辑模式
     */
    function toggleEditFolderOrder() {
        isEditingFolderOrder = !isEditingFolderOrder;

        // 更新按钮文字
        const btn = document.getElementById('btnEditFolderOrder');
        if (btn) {
            btn.innerHTML = isEditingFolderOrder
                ? '<span class="icon icon-lock"></span> <span data-i18n="panel.done_sort">' + t('panel.done_sort') + '</span>'
                : '<span class="icon icon-edit"></span> <span data-i18n="panel.sort">' + t('panel.sort') + '</span>';
            btn.classList.toggle('active', isEditingFolderOrder);
        }

        // Cancel active drag when toggling mode
        if (folderDrag && folderDrag.active) {
            cancelAnimationFrame(folderDrag.raf);
            if (folderDrag.ghost && folderDrag.ghost.parentNode) {
                folderDrag.ghost.parentNode.removeChild(folderDrag.ghost);
            }
            if (folderDrag.dragEl) {
                folderDrag.dragEl.style.position = '';
                folderDrag.dragEl.style.left = '';
                folderDrag.dragEl.style.top = '';
                folderDrag.dragEl.style.zIndex = '';
                folderDrag.dragEl.style.pointerEvents = '';
                folderDrag.dragEl.style.boxShadow = '';
            }
            folderDrag = null;
        }

        // 重新渲染文件夹树（应用/移除拖拽属性）
        lastFolderTreeHash = '';
        renderFolderTree();

        if (isEditingFolderOrder) {
            App.showToast(t('sidebar.sort_mode_enter'), 'info');
        } else {
            // 退出编辑模式时保存排序
            saveFolderOrder();
            App.showToast(t('sidebar.sort_saved'), 'success');
        }
    }

    /**
     * 更新编辑排序按钮的 UI 状态
     */
    function updateEditOrderButtonUI() {
        const btn = document.getElementById('btnEditFolderOrder');
        if (btn) {
            btn.innerHTML = isEditingFolderOrder
                ? '<span class="icon icon-lock"></span> <span data-i18n="panel.done_sort">' + t('panel.done_sort') + '</span>'
                : '<span class="icon icon-edit"></span> <span data-i18n="panel.sort">' + t('panel.sort') + '</span>';
            btn.classList.toggle('active', isEditingFolderOrder);
        }
    }

    // ==================== 文件夹操作 ====================

    /**
     * 打开系统文件夹选择器
     * ★ Wails 环境：使用原生系统对话框（runtime.OpenDirectoryDialog），
     *    直接获取绝对路径，无需用户手动输入，无浏览器安全限制。
     *    浏览器环境：回退到 File System Access API 或 webkitdirectory。
     */
    async function openSystemFolderPicker() {
        try {
            // ★ Wails 环境：使用原生系统对话框
            if (typeof WailsBridge !== 'undefined' && WailsBridge.isWails()) {
                const folderPath = await WailsBridge.selectFolder();
                if (!folderPath) {
                    // 用户取消了选择
                    return;
                }

                // ★ 乐观插入：在 RPC 调用之前立即将文件夹加入侧栏，用户立即看到反馈
                const folderName = folderPath.split(/[\\/]/).pop() || '未命名';

                // ★ 乐观插入前同步已有节点的展开状态到缓存，避免后续 DOM 重建时状态丢失
                for (const node of folderRoots) {
                    if (typeof node.expanded === 'boolean') {
                        expandedStateCache.set(node.path, node.expanded);
                    }
                }
                if (typeof Gallery !== 'undefined' && Gallery.addImportedRoot) {
                    Gallery.addImportedRoot({
                        rootId: folderPath,
                        path: folderPath,
                        name: folderName,
                        displayName: folderName
                    });
                }

                const normalizedNew = folderPath.replace(/\\/g, '/').toLowerCase();
                if (!folderRoots.some(r => (r.path || '').replace(/\\/g, '/').toLowerCase() === normalizedNew)) {
                    folderRoots.push({
                        name: folderName,
                        path: folderPath,
                        displayPath: folderPath,
                        displayName: folderName,
                        expanded: true,
                        imageCount: 0,
                        thumbCount: 0,
                        isRoot: true,
                        children: []
                    });
                    renderFolderTree();
                }

                // ★ 扫描已变为异步：Go 立刻返回，后台 goroutine 执行扫描
                const result = await WailsBridge.scanFolder(folderPath, 'mixed');
                if (!result || !result.success) {
                    // 扫描失败，移除乐观插入的节点
                    removeRootFromTree(folderPath);
                    renderFolderTree();
                    throw new Error((result && result.message) || '扫描文件夹失败');
                }

                App.showToast(result.message || `正在添加文件夹: ${folderName}`, 'success');

                // ★ RPC 成功后持久化 importedRoots 到后端
                if (typeof Gallery !== 'undefined' && Gallery.saveImportedRootsToServer) {
                    Gallery.saveImportedRootsToServer().catch(err =>
                        console.warn('[Sidebar] 持久化导入文件夹信息失败:', err.message)
                    );
                }

                // ★ 异步扫描：用 scan:complete 事件驱动后续操作
                //    事件回调中会刷新树、更新 count、自动切换文件夹
                pollScanProgress(folderPath, folderName);
                return;
            }

            // 浏览器环境：回退到 File System Access API
            if (typeof Gallery !== 'undefined' && Gallery.importFromDirectoryPicker) {
                const result = await Gallery.importFromDirectoryPicker();
                if (result) {
                    await refreshFolderTree();
                }
            } else {
                // 回退到 webkitdirectory
                if (hiddenFolderInput) {
                    hiddenFolderInput.click();
                }
            }
        } catch (err) {
            console.error('[Sidebar] 选择文件夹失败:', err);
            App.showToast(t('sidebar.select_folder_failed') + ': ' + err.message, 'error');
        }
    }

    /**
     * 处理 webkitdirectory 文件输入变化
     */
    async function handleFolderInputChange(event) {
        const files = event.target.files;
        if (!files || files.length === 0) return;

        try {
            if (typeof Gallery !== 'undefined' && Gallery.importFromFileList) {
                const result = await Gallery.importFromFileList(files);
                if (result && result.importedCount > 0) {
                    await refreshFolderTree();
                    App.showToast(`已导入 ${result.importedCount} 张图片`, 'success');
                } else {
                    App.showToast(t('toast.no_new_images'), 'info');
                }
            }
        } catch (err) {
            App.showToast(t('toast.import_failed_short') + ': ' + err.message, 'error');
        }

        // 重置 input，允许重复选择同一文件夹
        event.target.value = '';
    }

    /**
     * 移除文件夹路径
     */
    async function removeFolderPath(path) {
        if (!path) return;

        // 立即从 UI 中移除，无论后端调用结果如何
        removeRootFromTree(path);
        renderFolderTree();

        try {
            let ok = false;
            if (typeof WailsBridge !== 'undefined' && WailsBridge.isWails()) {
                try {
                    await WailsBridge.removeFolder(path);
                    ok = true;
                } catch (err) {
                    console.warn('[Sidebar] Wails 移除文件夹失败:', err.message);
                }
            } else {
                const response = await fetch('/api/remove-folder', {
                    method: 'POST',
                    headers: { 'Content-Type': 'application/json' },
                    body: JSON.stringify({ path: path })
                });
                ok = response.ok;
            }

            if (ok) {
                if (typeof Gallery !== 'undefined' && Gallery.removeImportedRoot) {
                    await Gallery.removeImportedRoot(path);
                }
                App.showToast(t('toast.folder_removed'), 'success');
            } else {
                if (typeof Gallery !== 'undefined' && Gallery.removeImportedRoot) {
                    const removed = await Gallery.removeImportedRoot(path);
                    if (removed) {
                        App.showToast(t('toast.folder_removed'), 'success');
                    } else {
                        App.showToast(t('toast.remove_failed'), 'error');
                    }
                }
            }
        } catch (err) {
            App.showToast(t('toast.remove_folder_failed') + ': ' + err.message, 'error');
        }

        // 延迟刷新，确保后端异步清理已完成
        setTimeout(() => refreshFolderTree(), 300);
    }

    /**
     * ★ 修复 Bug 1：直接从 folderRoots 中移除指定节点
     *    在 refreshFolderTree 之前执行，确保删除后导航栏立即响应
     *    通过规范化路径比对，解决斜杠风格不一致导致匹配失败的问题
     */
    function removeRootFromTree(path) {
        const normalized = (path || '').replace(/\\/g, '/').toLowerCase();
        folderRoots = folderRoots.filter(node => {
            const nodePath = (node.path || '').replace(/\\/g, '/').toLowerCase();
            return nodePath !== normalized && !nodePath.startsWith(normalized + '/');
        });
    }

    /**
     * ★ 后台轮询扫描进度：文件夹已显示在导航栏，等待后端扫描完成后更新 imageCount
     *
     * v2 重构：
     * - 用 GetFolderCount(folderPath) 替代 GetFolders()，不再每次轮询都重建整棵树
     * - 只在 count 变化时轻量更新当前节点的 imageCount（不走 refreshFolderTree）
     * - 只在最终完成时调用一次 refreshFolderTree，确保完整树刷新
     */
    function pollScanProgress(folderPath, folderName) {
        const normalizedTarget = (folderPath || '').replace(/\\/g, '/').toLowerCase();
        const maxWait = 120000;
        const interval = 800;
        const startTime = Date.now();
        let lastCount = -1;
        let stableRounds = 0;
        const stableThreshold = 3;
        let scanComplete = false;

        function updateLocalCount(count) {
            function updateNode(nodes) {
                for (const node of nodes) {
                    const nodePath = (node.path || '').replace(/\\/g, '/').toLowerCase();
                    if (nodePath === normalizedTarget) {
                        node.imageCount = count;
                        return true;
                    }
                    if (node.children && node.children.length > 0 && updateNode(node.children)) {
                        return true;
                    }
                }
                return false;
            }
            updateNode(folderRoots);
        }

        // 轻量更新 DOM 中的 count，不重建整棵树
        function updateDOMCount(count) {
            if (!folderTree) return;
            const rootNode = folderTree.querySelector('.tree-node[data-path="' + CSS.escape(folderPath) + '"]');
            if (rootNode) {
                const countEl = rootNode.querySelector(':scope > .tree-node-header > .tree-count');
                if (countEl) {
                    countEl.textContent = count;
                    countEl.dataset.total = count;
                }
            }
        }

        function finishPoll(count) {
            if (count >= 0) updateLocalCount(count);
            updateDOMCount(count);
            if (count > 0 && typeof Gallery !== 'undefined' && Gallery.filterByFolder) {
                if (Gallery._beginScanLoad) Gallery._beginScanLoad(folderPath);
                Gallery.filterByFolder(folderPath, folderName);
            }
            console.log('[Sidebar] 扫描完成:', folderPath, count, '张图片');
        }

        async function poll() {
            if (Date.now() - startTime > maxWait) {
                console.warn('[Sidebar] 扫描轮询超时:', folderPath);
                // 超时时做最后一次 getFolderCount 尝试
                try {
                    if (typeof WailsBridge !== 'undefined' && WailsBridge.isWails()) {
                        const finalCount = await WailsBridge.getFolderCount(folderPath);
                        if (finalCount >= 0) {
                            updateLocalCount(finalCount);
                            updateDOMCount(finalCount);
                            if (finalCount > 0 && typeof Gallery !== 'undefined' && Gallery.filterByFolder) {
                                if (Gallery._beginScanLoad) Gallery._beginScanLoad(folderPath);
                                await Gallery.filterByFolder(folderPath, folderName);
                            }
                            return;
                        }
                    }
                } catch (e) { /* ignore */ }
                await refreshFolderTree();
                return;
            }

            // scan:complete 已触发：获取最终 count 并退出
            if (scanComplete) {
                try {
                    if (typeof WailsBridge !== 'undefined' && WailsBridge.isWails()) {
                        const count = await WailsBridge.getFolderCount(folderPath);
                        if (count >= 0) {
                            updateLocalCount(count);
                            updateDOMCount(count);
                            if (count > 0 && typeof Gallery !== 'undefined' && Gallery.filterByFolder) {
                                if (Gallery._beginScanLoad) Gallery._beginScanLoad(folderPath);
                                await Gallery.filterByFolder(folderPath, folderName);
                            }
                            console.log('[Sidebar] 扫描完成 (scan:complete):', folderPath, count, '张图片');
                            return;
                        }
                    }
                } catch (e) { /* ignore */ }
                // 如果 getFolderCount 失败，回退到整树刷新
                await refreshFolderTree();
                return;
            }

            try {
                let count = -1;
                if (typeof WailsBridge !== 'undefined' && WailsBridge.isWails()) {
                    count = await WailsBridge.getFolderCount(folderPath);
                }

                if (count >= 0) {
                    // 轻量更新 DOM（只改 count 文字）
                    updateLocalCount(count);
                    updateDOMCount(count);

                    // 稳定检测：连续 N 次相同且 >0 即认为 countFilesQuick 已完成
                    if (count === lastCount && count > 0) {
                        stableRounds++;
                        if (stableRounds >= stableThreshold) {
                            // count 稳定了，但 scan:complete 还没来
                            // 继续等待 scan:complete（或超时），但降低轮询频率
                            console.log('[Sidebar] count 已稳定:', count, '，等待 scan:complete...');
                        }
                    } else {
                        stableRounds = 0;
                        lastCount = count;
                    }
                }
            } catch (e) { /* ignore */ }

            setTimeout(poll, interval);
        }

        // 监听后端 scan:complete 事件，标记后下一轮 poll 立即结束
        let onScanComplete = null;
        if (typeof WailsBridge !== 'undefined' && WailsBridge.isWails()) {
            onScanComplete = (data) => {
                const eventPath = (data.rootPath || '').replace(/\\/g, '/').toLowerCase();
                if (eventPath === normalizedTarget) {
                    scanComplete = true;
                    console.log('[Sidebar] 收到 scan:complete:', folderPath);
                }
            };
            const app = window['go'] && window['go']['main'] && window['go']['main']['App'];
            if (app && typeof app.EventsOn === 'function') {
                app.EventsOn('scan:complete', onScanComplete);
            } else {
                onScanComplete = null;
            }
        }
        // 包装 poll，在所有退出点移除事件监听
        const cleanupEvent = () => {
            if (onScanComplete) {
                const app = window['go'] && window['go']['main'] && window['go']['main']['App'];
                if (app && typeof app.EventsOff === 'function') {
                    app.EventsOff('scan:complete');
                }
                onScanComplete = null;
            }
        };
        const origPoll = poll;
        poll = async function() {
            const result = await origPoll();
            cleanupEvent();
            return result;
        };

        setTimeout(poll, 500);
    }

    // ==================== 标签树（Bookshelf Drag Reorder） ====================

    // 9 色系调色板
    const TAG_COLORS = [
        '#9b59b6', // 紫色
        '#4a9eff', // 蓝色
        '#1abc9c', // 青绿
        '#2ecc71', // 绿色
        '#f39c12', // 琥珀
        '#e94560', // 珊瑚
        '#e91e63', // 粉色
        '#e74c3c', // 红色
        '#7f8c8d'  // 灰色
    ];

    // 预设图标
    const TAG_ICONS_CATEGORIES = [
        { name: '表情', icons: ['😀','😃','😄','😁','😆','😅','🤣','😂','🙂','😊','😇','🥰','😍','🤩','😘','😗','😚','😋','😛','😜','🤪','😝','🤑','🤗','🤭','🤫','🤔','🤐','🤨','😐','😑','😶','😏','😒','🙄','😬','🤥','😌','😔','😪','😴','😷','🤒','🤕','🥵','🥶','😵','🤯','😎','😲','😳','🥺','😢','😭','😱','😖','😣','😤','😡','🤬','😈','👿','💀','💩','🤡','👻','👽','🤖','🎃','😺','😸','😹','😻','😼','😽','🙀','😿','😾'] },
        { name: '手势', icons: ['👋','🤚','🖐','✋','🖖','👌','🤌','🤏','✌','🤞','🤟','🤘','🤙','👈','👉','👆','🖕','👇','☝','👍','👎','✊','👊','🤛','🤜','👏','🙌','👐','🤲','🤝','🙏','✍','💅','🤳','💪','🦵','🦶','👂','👃','🧠','🦷','👀','👁','👅','👄'] },
        { name: '人物', icons: ['👶','👧','👦','👩','👨','👵','👴','🧑','👲','👳','🧔','👱','👰','🤵','🦸','🦹','🧙','🧚','🧛','🧜','🧝','💂','🤴','👸','🤰','🤱','👼','🎅','🤶','👮','🕵','👷','🤴','👸','💁','🙋','🙆','🙅','🤷','🤦','🙍','🙎','💆','💇','🧖','🛀','🛌','🚶','🏃','🧗','🏄','🏊','⛹','🏋','🚴','🚵','🤸','🤼','🤽','🤾','🤺','⛷','🏂','🏌','🏄','🚣','🏇'] },
        { name: '自然', icons: ['🐵','🐒','🦍','🐶','🐕','🐩','🐺','🦊','🐱','🐈','🦁','🐯','🐅','🐆','🐴','🐎','🦄','🦓','🦌','🐮','🐂','🐃','🐄','🐷','🐖','🐗','🐑','🐏','🐐','🐪','🐫','🦙','🦒','🐘','🦏','🦛','🐭','🐁','🐀','🐹','🐰','🐇','🐿','🦔','🦇','🐻','🐨','🐼','🐾','🦃','🐔','🐓','🐣','🐤','🐥','🐦','🐧','🕊','🦅','🦆','🦢','🦉','🦚','🦜','🐸','🐊','🐢','🦎','🐍','🐲','🐉','🦕','🦖','🐳','🐋','🐬','🐟','🐠','🐡','🦈','🐙','🐚','🦀','🦞','🦐','🦑','🐌','🦋','🐛','🐜','🐝','🐞','🦗','🕷','🦂','🦟','🦠'] },
        { name: '植物', icons: ['💐','🌸','💮','🏵','🌹','🥀','🌺','🌻','🌼','🌷','🌱','🌲','🌳','🌴','🌵','🌾','🌿','☘','🍀','🍁','🍂','🍃','🍄','🌰','🎍','🎋','🌰','🌱','🌿','🍀'] },
        { name: '食物', icons: ['🍏','🍎','🍐','🍊','🍋','🍌','🍉','🍇','🍓','🫐','🍈','🍒','🍑','🥭','🍍','🥥','🥝','🍅','🍆','🥑','🥦','🥬','🥒','🌶','🫑','🌽','🥕','🧄','🧅','🥔','🍠','🥐','🍞','🥖','🥨','🧀','🥚','🍳','🧈','🥞','🧇','🥓','🥩','🍗','🍖','🦴','🌭','🍔','🍟','🍕','🥪','🥙','🧆','🌮','🌯','🫔','🥗','🥘','🫕','🥫','🍝','🍜','🍲','🍛','🍣','🍱','🥟','🦪','🍤','🍙','🍚','🍘','🍥','🥠','🥮','🍢','🍡','🍧','🍨','🍦','🥧','🧁','🍰','🎂','🍮','🍭','🍬','🍫','🍿','🍩','🍪','🌰','🥜','🍯','🥤','🧋','🍵','☕','🍼','🥛','🧃','🥂','🍷','🍸','🍹','🍺','🍻','🧊'] },
        { name: '物品', icons: ['⌚','📱','💻','⌨','🖥','🖨','🖱','🖲','💾','💿','📀','🎥','📷','📹','📺','📻','🎙','🎚','🎛','⏰','⌛','📡','🔋','🔌','💡','🔦','🕯','🪔','🧯','🗑','📋','📎','🖇','📏','📐','✂','🔒','🔓','🔑','🔨','⛏','🪓','🔧','🔩','🔗','⛓','🧲','🧪','🧫','🧬','🔭','🔬','📿','🧿','💎','💊','💉','🩸','🧴','🧽','🧹','🧺','🧻','🧼','🪥','🪒','🪣','🚿','🛁','🛒','🎁','🎈','🎉','🎊','🎀','🏆','🏅','🎖','🎗','🏷','📌','📍','🧷','🧵','🧶'] },
        { name: '星心', icons: ['⭐','🌟','🌠','💫','✨','✴','✳','❇','❄','💥','🔥','💧','💦','💨','💣','💬','💭','🗯','❤','🧡','💛','💚','💙','💜','🖤','🤍','🤎','💔','❣','💕','💞','💓','💗','💖','💘','💝','💟','♥','♡','★','☆','✦','✧','✩','✪','✫','✬','✭','✮','✯','☄','⁂','≛','⍟','⍣','⊛'] },
        { name: '符号', icons: ['☑','✓','✔','✗','✘','☐','☒','⚠','⚡','☢','☣','⬆','⬇','⬅','➡','↗','↘','↙','↖','↕','↔','🔄','🔃','🔙','🔚','🔛','🔜','🔝','⏫','⏬','⏪','⏩','◀','▶','⏮','⏭','🔀','🔁','🔂','🔴','🟠','🟡','🟢','🔵','🟣','🟤','⚫','⚪','🟥','🟧','🟨','🟩','🟦','🟪','⬛','⬜','🔶','🔷','🔸','🔹','🔺','🔻','🔼','🔽','♻','✅','❌','❓','❔','❕','❗','➕','➖','➗','✖','♾','©','®','™','💲','💰','🪙','💎'] }
    ];

    function buildIconPickerHTML() {
        var h = '<div class="tag-icon-grid"><span class="tag-icon-option none" data-icon="">无</span></div>';
        for (var c = 0; c < TAG_ICONS_CATEGORIES.length; c++) {
            var cat = TAG_ICONS_CATEGORIES[c];
            h += '<div class="tag-icon-cat">';
            h += '<div class="tag-icon-cat-name">' + cat.name + '</div>';
            h += '<div class="tag-icon-grid">';
            for (var i = 0; i < cat.icons.length; i++) {
                h += '<span class="tag-icon-option" data-icon="' + cat.icons[i] + '">' + cat.icons[i] + '</span>';
            }
            h += '</div></div>';
        }
        return h;
    }

    // Bookshelf-style drag: dragged item floats above; others stay in place.
    // On drop, reorder with a FLIP animation (CSS transition, no bounce).

    const DRAG = {
        liftScale: 1.08,
        settleMs: 200,
        hoverDelay: 500,  // ms to hover over a sibling before nesting underneath
        hitPaddingX: 40,  // extra horizontal hit area for narrow tags
        hitPaddingY: 8   // extra vertical hit area
    };

    // Tag edit mode state
    let isEditingTags = false;
    let selectedTagIds = new Set();

    // Global drag state (one active drag at a time)
    let tagDrag = null;

    // 标签树展开状态 { parentId: true }
    let tagExpanded = {};

    async function refreshTagTree() {
        try {
            if (typeof Storage !== 'undefined' && Storage.getAllTags) {
                tags = await Storage.getAllTags();
            }
            await loadTagOrder();
            await loadTagExpandedStates();
            renderTagTree();
            tagTreeLoaded = true;
            window.dispatchEvent(new CustomEvent('tags-changed'));
        } catch (err) {
            console.error('[Sidebar] 刷新标签树失败:', err);
        }
    }

    function buildTagTree() {
        // 从 tags 平坦数组构建树，返回 [{ tag, children: [{ tag, children: [...] }] }]
        const map = new Map();
        const roots = [];
        for (const t of tags) {
            map.set(t.id, { tag: t, children: [] });
        }
        for (const t of tags) {
            const node = map.get(t.id);
            if (t.parentId && map.has(t.parentId)) {
                map.get(t.parentId).children.push(node);
            } else {
                // parentId 无效或为空 → 根级
                roots.push(node);
            }
        }
        return roots;
    }

    function getTagDescendantIds(parentId) {
        const ids = [];
        const stack = [parentId];
        while (stack.length) {
            const pid = stack.pop();
            for (const t of tags) {
                if (t.parentId === pid) {
                    ids.push(t.id);
                    stack.push(t.id);
                }
            }
        }
        return ids;
    }

    function isDescendantOf(ancestorId, descendantId) {
        return getTagDescendantIds(ancestorId).includes(descendantId);
    }

    function renderTagTree() {
        if (!tagTree) return;

        if (tagDrag) {
            cancelAnimationFrame(tagDrag.raf);
            tagDrag = null;
        }

        // Clean stale selections
        const currentIds = new Set(tags.map(t => t.id));
        for (const id of selectedTagIds) {
            if (!currentIds.has(id)) selectedTagIds.delete(id);
        }

        if (tags.length === 0) {
            tagTree.innerHTML = '<p class="placeholder-text">' + t('sidebar.no_tags_hint') + '</p>';
            return;
        }

        const container = document.createElement('div');
        container.className = 'tag-tree-container';
        if (isEditingTags) {
            container.classList.add('edit-tags');
        }
        if (window.tagDropdownColumnMode === 2) {
            container.classList.add('multi-col');
        }

        const roots = buildTagTree();
        let flatIndex = 0;
        function renderNodes(nodes, depth) {
            for (const node of nodes) {
                const item = createTagItem(node.tag, flatIndex++, depth);
                container.appendChild(item);
                const hasChildren = node.children.length > 0;
                if (hasChildren) {
                    const isExpanded = tagExpanded[node.tag.id] !== false; // 默认展开
                    tagExpanded[node.tag.id] = isExpanded;
                    const childWrap = document.createElement('div');
                    childWrap.className = 'tag-children';
                    childWrap.dataset.parentId = node.tag.id;
                    childWrap.style.display = isExpanded ? '' : 'none';
                    childWrap.style.setProperty('--child-depth', depth + 1);
                    renderNodes(node.children, depth + 1);
                    // Move children (and their .tag-children wrappers) into wrapper
                    let next = item.nextSibling;
                    while (next && next.dataset && next.dataset.tagDepth === String(depth + 1)) {
                        const toMove = next;
                        const afterMove = toMove.nextSibling; // captured before moving
                        childWrap.appendChild(toMove);
                        // If the very next element is this child's .tag-children wrapper, move it too
                        if (afterMove && afterMove.classList && afterMove.classList.contains('tag-children')
                            && afterMove.dataset.parentId === toMove.dataset.tagId) {
                            const wrapEl = afterMove;
                            next = wrapEl.nextSibling; // advance past the wrapper, still in original parent
                            childWrap.appendChild(wrapEl);
                        } else {
                            next = afterMove;
                        }
                    }
                    // Mark last child in wrapper
                    const lastChild = childWrap.querySelector(':scope > .tag-item:last-of-type');
                    if (lastChild) lastChild.classList.add('tag-last-child');
                    // Insert wrapper after parent
                    item.parentNode.insertBefore(childWrap, item.nextSibling);
                    // Update toggle indicator
                    updateTagToggle(item, isExpanded);
                }
            }
        }
        renderNodes(roots, 0);

        // Mark last root-level tag for tree line └ turn
        var rootItems = container.querySelectorAll(':scope > .tag-item');
        if (rootItems.length > 0) {
            rootItems[rootItems.length - 1].classList.add('tag-last-child');
        }

        tagTree.innerHTML = '';
        tagTree.appendChild(container);

        if (isEditingTags) {
            initTagDrag(container);
            updateBatchBar();
        }
    }

    function setTagExpandedRecursive(tagId, isExp) {
        tagExpanded[tagId] = isExp;
        const wrapper = tagTree.querySelector(`.tag-children[data-parent-id="${tagId}"]`);
        if (wrapper) {
            wrapper.style.display = isExp ? '' : 'none';
            if (!isExp) {
                // When collapsing, recursively collapse all descendant tags
                const childTags = wrapper.querySelectorAll(':scope > .tag-item');
                for (const child of childTags) {
                    const childId = child.dataset.tagId;
                    if (childId) setTagExpandedRecursive(childId, false);
                }
            }
        }
        saveTagExpandedStates();
    }

    async function saveTagExpandedStates() {
        try {
            if (typeof Storage !== 'undefined' && Storage.setSetting) {
                await Storage.setSetting('tag_expanded', tagExpanded);
            }
        } catch (e) { /* 静默 */ }
    }

    async function loadTagExpandedStates() {
        try {
            if (typeof Storage !== 'undefined' && Storage.getSetting) {
                const saved = await Storage.getSetting('tag_expanded', {});
                if (saved && typeof saved === 'object') {
                    tagExpanded = saved;
                }
            }
        } catch (e) { /* 静默 */ }
    }

    function updateTagToggle(item, expanded) {
        let toggle = item.querySelector('.tag-tree-toggle');
        if (!toggle) {
            toggle = document.createElement('span');
            toggle.className = 'tag-tree-toggle';
            toggle.addEventListener('click', (e) => {
                e.stopPropagation();
                e.preventDefault();
                const tagId = item.dataset.tagId;
                const isExp = tagExpanded[tagId] !== true;
                setTagExpandedRecursive(tagId, isExp);
                updateTagToggle(item, isExp);
            });
            toggle.addEventListener('pointerdown', (e) => {
                e.stopPropagation();
            });
            item.insertBefore(toggle, item.firstChild);
        }
        toggle.textContent = '>';
        toggle.classList.toggle('expanded', expanded);
    }

    function createTagItem(tag, index, depth = 0) {
        const item = document.createElement('span');
        item.className = 'tag-item tag-badge';
        item.dataset.tagId = tag.id;
        item.dataset.tagIndex = index;
        item.dataset.tagDepth = depth;
        if (tag.parentId) {
            item.dataset.parentTagId = tag.parentId;
        }
        item.style.marginLeft = (depth * 20) + 'px';
        if (depth > 0) {
            item.classList.add('has-parent');
            item.style.setProperty('--tag-depth', depth);
        }

        const isAvatar = tag.tagType === 'avatar';
        const isHtml = tag.tagType === 'html';

        if (isHtml) {
            // HTML tag: 内容通过 transform:scale 等比缩放适配容器
            item.classList.add('tag-item-html');
            const w = tag.htmlWidth || 120;
            const h = tag.htmlHeight || 40;
            item.style.cssText = `display:inline-flex;align-items:center;gap:2px;margin-left:${depth * 20}px;flex-shrink:0;`;
            if (depth > 0) item.style.setProperty('--tag-depth', depth);
            const wrapper = document.createElement('div');
            wrapper.style.cssText = `width:${w}px;height:${h}px;overflow:hidden;flex-shrink:0;pointer-events:none;`;
            const inner = document.createElement('div');
            inner.style.cssText = 'position:absolute;top:0;left:0;transform-origin:0 0;';
            // 用唯一 scope 包裹，防止 CSS 污染其他标签
            const scopeId = 'tag-scope-' + tag.id;
            wrapper.setAttribute('data-tag-scope', scopeId);
            // 先对完整 HTML 做 URL 替换（<img src>, <image href> + CSS url()）
            let processedCode = (typeof WailsBridge !== 'undefined' && WailsBridge.fixRelativeUrls)
                ? WailsBridge.fixRelativeUrls(tag.htmlCode || '') : (tag.htmlCode || '');
            let scopedHtml = processedCode.replace(/<style([^>]*)>/g, (_, attrs) => {
                return '<style' + attrs + ' data-scope="' + scopeId + '">';
            });
            inner.innerHTML = scopedHtml;
            // scope all CSS rules to this tag only
            inner.querySelectorAll('style[data-scope]').forEach(styleEl => {
                const raw = styleEl.textContent;
                if (!raw) return;
                const scoped = raw.replace(/([^{}]*\{)/g, (rule) => {
                    const trimmed = rule.trim();
                    if (/^@|^\d+(\.\d+)?%|^(from|to)\b/i.test(trimmed)) return rule;
                    if (/\b(html|body|:root)\b|\*/.test(trimmed)) return '';
                    return rule.replace(/(^|,)\s*/g, (sep) => {
                        return sep + '[data-tag-scope="' + scopeId + '"] ';
                    });
                });
                styleEl.textContent = (typeof WailsBridge !== 'undefined' && WailsBridge.fixRelativeUrls)
                    ? WailsBridge.fixRelativeUrls(scoped) : scoped;
                styleEl.removeAttribute('data-scope');
            });
            wrapper.appendChild(inner);
            // 测量自然尺寸，等比缩放适配容器
            requestAnimationFrame(() => {
                const rect = inner.getBoundingClientRect();
                const nw = rect.width || w;
                const nh = rect.height || h;
                const scale = Math.min(w / nw, h / nh, 1); // 只缩小不放大
                inner.style.transform = `scale(${scale})`;
                // 居中：计算缩放后偏移
                const scaledW = nw * scale;
                const scaledH = nh * scale;
                inner.style.left = ((w - scaledW) / 2) + 'px';
                inner.style.top = ((h - scaledH) / 2) + 'px';
            });
            inner.querySelectorAll('script').forEach(s => {
                try { eval(s.textContent); } catch(e) {}
            });
            item.appendChild(wrapper);
            // Fall through for drag handle + delete btn + click handler below
        } else if (isAvatar) {
            // Avatar tag: image + name
            item.classList.add('tag-item-avatar');
            const itemBg = tag.bgColor || 'var(--bg-secondary)';
            const shape = tag.shape || 'round';
            const rad = shape === 'sharp' ? 2 : shape === 'soft' ? 6 : 14;
            const thumbRad = shape === 'sharp' ? 2 : shape === 'soft' ? 4 : 10;
            const isImageOnly = !tag.name || tag.showName === false;
            const avatarThumbSize = typeof tag.avatarThumbSize === 'number' ? tag.avatarThumbSize : 40;
            item.style.cssText = `display:inline-flex;align-items:center;gap:0;background:${itemBg};border:0.5px solid var(--border-color);border-radius:${rad}px;padding:0;position:relative;margin-left:${depth * 20}px;`;
            if (depth > 0) item.style.setProperty('--tag-depth', depth);

            if (tag.avatarData) {
                const avThumb = document.createElement('img');
                avThumb.src = typeof WailsBridge !== 'undefined' && WailsBridge.getAvatarUrl
                    ? WailsBridge.getAvatarUrl(tag.avatarData) : tag.avatarData;
                avThumb.style.cssText = `width:${avatarThumbSize}px;height:${avatarThumbSize}px;border-radius:${isImageOnly ? rad : thumbRad}px;object-fit:cover;flex-shrink:0;`;
                item.appendChild(avThumb);
            } else {
                const avInit = document.createElement('span');
                avInit.style.cssText = `width:${avatarThumbSize}px;height:${avatarThumbSize}px;border-radius:${isImageOnly ? rad : thumbRad}px;display:flex;align-items:center;justify-content:center;font-size:1.17em;font-weight:500;flex-shrink:0;background:${itemBg};color:${tag.color || '#fff'};`;
                avInit.textContent = (tag.name || '?')[0].toUpperCase();
                item.appendChild(avInit);
            }

            if (!isImageOnly) {
                const label = document.createElement('span');
                label.textContent = tag.name;
                const avatarBg = tag.bgColor || 'var(--bg-secondary)';
                label.style.cssText = `font-size:var(--avatar-text-size);font-weight:500;padding:0 0.83em;white-space:nowrap;overflow:hidden;text-overflow:ellipsis;max-width:8.33em;color:${tag.color || '#ffffff'};background:${avatarBg};border-radius:0 ${rad}px ${rad}px 0;`;
                item.appendChild(label);
            }
        } else {
            TagStyle.apply(item, tag);
        }

        if (isEditingTags) {
            const handle = document.createElement('span');
            handle.className = 'tag-drag-handle';
            handle.textContent = '⠿';
            handle.title = t('sidebar.drag_sort');
            item.appendChild(handle);
        }

        const isIconOnly = tag.iconOnly || (!tag.name && tag.icon);
        if (tag.icon && !isAvatar) {
            const iconSize = tag.iconOnlySize ? tag.iconOnlySize : null;
            if (tag.icon.indexOf('data:') === 0) {
                const iconImg = document.createElement('img');
                iconImg.className = 'tag-icon-img' + (isIconOnly ? ' tag-icon-only-img' : '');
                iconImg.src = tag.icon;
                iconImg.alt = '';
                if (iconSize) {
                    iconImg.style.height = iconSize + 'px';
                }
                item.appendChild(iconImg);
            } else {
                const iconSpan = document.createElement('span');
                iconSpan.className = 'tag-icon' + (isIconOnly ? ' tag-icon-only-text' : '');
                iconSpan.textContent = tag.icon;
                if (iconSize) {
                    iconSpan.style.fontSize = iconSize + 'px';
                }
                item.appendChild(iconSpan);
            }
        }

        if (isIconOnly || isHtml) {
            // icon-only or html tag: no text label
        } else if (tag.showName !== false) {
            // 勾选了"显示名称"才渲染名称
            const label = document.createElement('span');
            label.textContent = tag.name;
            if (isAvatar) {
                // Already added label above, replace this one
            } else {
                item.appendChild(label);
            }
        } // end else (not iconOnly)

        // ★ 文件夹标签：显示文件夹图标
        if (tag.linkedFolder) {
            const folderIcon = document.createElement('span');
            folderIcon.className = 'tag-folder-icon';
            folderIcon.innerHTML = '<span class="icon icon-browse"></span>';
            folderIcon.title = t('sidebar.linked_to_folder').replace('{folder}', tag.linkedFolder);
            item.appendChild(folderIcon);
        }

        const delBtn = document.createElement('button');
        delBtn.className = 'tag-delete-btn';
        delBtn.innerHTML = '<span class="icon icon-close"></span>';
        delBtn.title = t('sidebar.delete_tag_title').replace('{name}', tag.name || '');
        delBtn.addEventListener('click', (e) => {
            e.stopPropagation();
            showDeleteTagDialog(tag);
        });
        delBtn.addEventListener('pointerdown', (e) => {
            e.stopPropagation();
        });
        item.appendChild(delBtn);

        // Restore selection state
        if (selectedTagIds.has(tag.id)) {
            item.classList.add('selected-for-edit');
        }

        item.addEventListener('click', () => {
            if (tagDrag && tagDrag.didMove) return;
            if (isEditingTags) {
                // In edit-tags mode, clicking the tag body toggles selection
                toggleTagSelection(tag, item);
                return;
            }
            tagTree.querySelectorAll('.tag-item.active').forEach(el => {
                el.classList.remove('active');
            });
            item.classList.add('active');
            if (onTagSelected) onTagSelected(tag);
        });

        // Double-click in edit-tags mode opens edit dialog
        item.addEventListener('dblclick', (e) => {
            if (!isEditingTags) return;
            e.stopPropagation();
            if (tagDrag && tagDrag.didMove) return;
            showEditTagDialog(tag);
        });

        return item;
    }

    // ==================== Tag Selection Helpers ====================

    function toggleTagSelection(tag, item) {
        if (selectedTagIds.has(tag.id)) {
            selectedTagIds.delete(tag.id);
            item.classList.remove('selected-for-edit');
        } else {
            selectedTagIds.add(tag.id);
            item.classList.add('selected-for-edit');
        }
        updateBatchBar();
    }

    function updateBatchBar() {
        const container = tagTree.querySelector('.tag-tree-container');
        if (!container) return;
        let bar = container.querySelector('.tag-edit-batch-bar');
        if (!bar) {
            bar = document.createElement('div');
            bar.className = 'tag-edit-batch-bar';
            container.appendChild(bar);
        }
        const count = selectedTagIds.size;
        if (isEditingTags) {
            bar.classList.add('active');
            bar.innerHTML = `
                <span>${t('sidebar.selected_tags', { n: count })}</span>
                ${count > 0 ? `<button class="btn-small" id="btnBatchEditTags"><span class="icon icon-edit"></span> ${t("sidebar.edit")}</button>
                <button class="btn-small" id="btnBatchDupTag"><span class="icon icon-copy"></span> ${t("sidebar.duplicate")}</button>
                <button class="btn-small btn-danger" id="btnBatchDeleteTags"><span class="icon icon-delete"></span> ${t("sidebar.batch_delete")}</button>
                <button class="btn-small" id="btnClearTagSelection">${t("sidebar.clear_selection")}</button>` : ''}
            `;
            // 清空已移除 slider 的引用变量
            var tagSlider = null;
            var avaSlider = null;
            var avaTextSlider = null;
            if (count > 0) {
            bar.querySelector('#btnBatchDeleteTags')?.addEventListener('click', async () => {
                if (!confirm(t('sidebar.confirm_delete_tag_multi').replace('{n}', count))) return;
                try {
                    for (const tagId of selectedTagIds) {
                        await Storage.deleteTag(tagId);
                    }
                    selectedTagIds = new Set();
                    await refreshTagTree();
                    App.showToast(t('toast.tags_batch_deleted'), 'success');
                } catch (err) {
                    App.showToast(t('toast.batch_delete_failed') + ': ' + err.message, 'error');
                }
            });
            bar.querySelector('#btnClearTagSelection').addEventListener('click', () => {
                selectedTagIds = new Set();
                renderTagTree();
            });
            bar.querySelector('#btnBatchEditTags').addEventListener('click', () => {
                const selectedTags = tags.filter(t => selectedTagIds.has(t.id));
                if (selectedTags.length === 1) {
                    showEditTagDialog(selectedTags[0]);
                } else if (selectedTags.length > 1) {
                    App.showToast(t('sidebar.edit_single_tag_only'), 'warning');
                }
            });
            bar.querySelector('#btnBatchDupTag').addEventListener('click', async () => {
                const selectedTags = tags.filter(t => selectedTagIds.has(t.id));
                if (selectedTags.length !== 1) {
                    App.showToast(t('toast.copy_single_tag_only'), 'warning');
                    return;
                }
                try {
                    const src = selectedTags[0];
                    const dup = Object.assign({}, src);
                    delete dup.id;
                    dup.name = (dup.name || '') + ' (副本)';
                    await Storage.addTag(dup);
                    selectedTagIds = new Set();
                    await refreshTagTree();
                    App.showToast(t('toast.tag_copied'), 'success');
                } catch (err) {
                    App.showToast(t('toast.copy_tag_failed') + ': ' + (err.message || err), 'error');
                }
            });
            } // end if (count > 0)
        } else {
            bar.classList.remove('active');
            bar.innerHTML = '';
        }
    }

    // ==================== Tag Edit Mode Toggle ====================

    function toggleEditTags() {
        isEditingTags = !isEditingTags;
        selectedTagIds = new Set();
        const btn = document.getElementById('btnEditTags');
        if (btn) {
            btn.innerHTML = isEditingTags
                ? '<span class="icon icon-lock"></span> <span data-i18n="panel.done">' + t('panel.done') + '</span>'
                : '<span class="icon icon-edit"></span> <span data-i18n="panel.edit">' + t('panel.edit') + '</span>';
            btn.classList.toggle('active', isEditingTags);
        }
        renderTagTree();
        if (isEditingTags) {
            App.showToast(t('sidebar.tag_edit_mode_enter'), 'info');
        } else {
            saveTagOrder();
            App.showToast(t('sidebar.tag_edit_saved'), 'success');
        }
    }

    // ==================== Bookshelf Drag Engine (Ghost Placeholder) ====================
    //
    // During drag: the dragged tag is pulled out of flex flow (position:absolute)
    // and follows the pointer. A ghost placeholder (same size, dashed outline) sits
    // at the insertion point in the flex flow — other tags automatically re-wrap
    // around it. On drop: ghost is removed, everything FLIP-animates into place.

    function initTagDrag(container) {
        container.addEventListener('pointerdown', (e) => {
            if (!isEditingTags) return;
            // Only drag via the drag handle, not the whole tag
            if (!e.target.closest('.tag-drag-handle')) return;
            const item = e.target.closest('.tag-item');
            if (!item) return;

            e.preventDefault();
            startTagDrag(e, container, item);
        });
    }

    function startTagDrag(e, container, dragEl) {
        if (tagDrag && tagDrag.active) return;

        // Determine sibling group: the DOM parent that contains only siblings
        const groupEl = dragEl.parentNode.classList.contains('tag-children')
            ? dragEl.parentNode
            : container;
        const siblingItems = Array.from(groupEl.querySelectorAll(':scope > .tag-item'));
        if (siblingItems.length === 0) return;

        const dragTagId = dragEl.dataset.tagId;
        const dragSiblingIdx = siblingItems.indexOf(dragEl);
        const dragRect = dragEl.getBoundingClientRect();
        const groupRect = groupEl.getBoundingClientRect();

        // Collapse children if dragging a parent tag
        let collapsedChildWrap = null;
        const childWrap = getTagChildWrap(dragTagId);
        if (childWrap && childWrap.style.display !== 'none') {
            collapsedChildWrap = childWrap;
            childWrap.style.display = 'none';
        }

        // Snapshot sibling positions
        const positions = new Array(siblingItems.length);
        for (let i = 0; i < siblingItems.length; i++) {
            const r = siblingItems[i].getBoundingClientRect();
            positions[i] = {
                el: siblingItems[i],
                restX: r.left - groupRect.left,
                restY: r.top - groupRect.top,
                width: r.width,
                height: r.height
            };
        }

        // Create ghost placeholder — inherit the dragged tag's indent so it stays at the correct level
        const ghost = document.createElement('span');
        ghost.className = 'tag-ghost-placeholder';
        ghost.style.width = dragRect.width + 'px';
        ghost.style.height = dragRect.height + 'px';
        ghost.style.marginLeft = dragEl.style.marginLeft; // stay at same indent level
        let tagBg = getComputedStyle(dragEl).backgroundColor;
        if (!tagBg || tagBg === 'rgba(0, 0, 0, 0)' || tagBg === 'transparent') {
            tagBg = getComputedStyle(dragEl).color || 'rgb(74, 158, 255)';
        }
        ghost.style.borderColor = tagBg;
        ghost.style.background = tagBg.replace('rgb', 'rgba').replace(')', ', 0.22)');
        ghost.style.outline = '2px solid ' + tagBg.replace('rgb', 'rgba').replace(')', ', 0.10)');
        ghost.style.outlineOffset = '2px';
        groupEl.insertBefore(ghost, dragEl);

        // Save grab offset so the tag doesn't jump to center on mousedown
        var grabOffsetX = e.clientX - dragRect.left;
        var grabOffsetY = e.clientY - dragRect.top;

        // Pull dragged tag out of flex flow
        dragEl.style.position = 'absolute';
        dragEl.style.zIndex = '20';
        dragEl.style.pointerEvents = 'none';
        dragEl.style.transition = 'none';
        dragEl.style.boxShadow = '0 6px 20px rgba(0,0,0,0.35)';
        dragEl.style.scale = String(DRAG.liftScale);
        dragEl.style.left = (e.clientX - groupRect.left - grabOffsetX) + 'px';
        dragEl.style.top = (e.clientY - groupRect.top - grabOffsetY) + 'px';

        // Fix tree line └ turn: dragEl stays in DOM but is now absolute.
        dragEl.classList.remove('tag-last-child');
        syncTagLastChild(groupEl, dragEl);

        tagDrag = {
            active: true,
            didMove: false,
            container,
            groupEl,
            siblingItems: positions,
            dragTagId,
            dragSiblingIdx,
            parentId: dragEl.dataset.parentTagId || null,
            dragEl,
            ghost,
            dragWidth: dragRect.width,
            dragHeight: dragRect.height,
            grabOffsetX: grabOffsetX,
            grabOffsetY: grabOffsetY,
            pointerX: e.clientX,
            pointerY: e.clientY,
            startPointerX: e.clientX,
            startPointerY: e.clientY,
            startDepth: parseInt(dragEl.dataset.tagDepth) || 0,
            startParentId: dragEl.dataset.parentTagId || null,
            raf: null,
            lastInsertBefore: dragEl,
            collapsedChildWrap,
            _logFrame: 0,
            _lastBefore: null,
            _lastType: null,
            _lastParent: null,
            tempChildWrap: null,
            _nestHoverTarget: null,
            _nestHoverStart: 0,
            _nestHoverLeaveTime: 0,
            _promoteEnterTime: 0,
            _lastSide: null
        };

        document.addEventListener('pointermove', onTagPointerMove, { passive: false });
        document.addEventListener('pointerup', onTagPointerUp, { once: true });
        document.addEventListener('pointercancel', onTagPointerUp, { once: true });

        groupEl.style.touchAction = 'none';
        container.classList.add('is-dragging');

        tagDrag.raf = requestAnimationFrame(dragLoop);
    }

    function getTagChildWrap(tagId) {
        const el = tagTree.querySelector('.tag-children[data-parent-id="' + tagId + '"]');
        return el;
    }

    // Find the .tag-children wrapper that immediately follows a tag item within a group
    function findChildWrapInGroup(tagEl, groupEl) {
        let next = tagEl.nextElementSibling;
        if (next && next.classList.contains('tag-children') && next.dataset.parentId === tagEl.dataset.tagId) {
            return next;
        }
        // Fallback: search within group
        const wraps = groupEl.querySelectorAll(':scope > .tag-children');
        for (const w of wraps) {
            if (w.dataset.parentId === tagEl.dataset.tagId) return w;
        }
        return null;
    }

    function onTagPointerMove(e) {
        if (!tagDrag || !tagDrag.active) return;
        e.preventDefault();

        const dx = e.clientX - tagDrag.pointerX;
        const dy = e.clientY - tagDrag.pointerY;
        if (Math.abs(dx) + Math.abs(dy) > 3) {
            tagDrag.didMove = true;
        }

        tagDrag.pointerX = e.clientX;
        tagDrag.pointerY = e.clientY;
    }

    // Drop target: depth-based snapping with hysteresis.
    // ==================== Tag Drag (pure Y-sort + simple L/R depth switch) ====================

    function dragLoop() {
        if (!tagDrag || !tagDrag.active) return;
        var drag = tagDrag;
        var gr = drag.groupEl.getBoundingClientRect();
        drag.dragEl.style.left = (drag.pointerX - gr.left - drag.grabOffsetX) + 'px';
        drag.dragEl.style.top = (drag.pointerY - gr.top - drag.grabOffsetY) + 'px';
        if (drag.didMove) {
            var t = computeDropTarget(drag);
            drag._logFrame++;
            if (drag._logFrame % 15 === 0 || t.type !== drag._lastType || t.parentEl !== drag._lastParent) {
                var dx = drag.pointerX - drag.startPointerX, dy = drag.pointerY - drag.startPointerY;
                var dNow = t.parentEl ? (parseInt(t.parentEl.dataset.tagDepth)+1) : 0;
                console.log('[drag]', drag.dragTagId.slice(-8),
                    'Δ('+dx.toFixed(0)+','+dy.toFixed(0)+')',
                    'L'+drag.startDepth+'→L'+dNow,
                    t.type,
                    'parent='+(t.parentEl?t.parentEl.dataset.tagId.slice(-8):'ROOT'));
            }
            if (t.beforeEl !== drag._lastBefore || t.type !== drag._lastType || t.parentEl !== drag._lastParent) {
                drag._lastBefore = t.beforeEl;
                drag._lastType = t.type;
                drag._lastParent = t.parentEl;
                // move ghost
                if (t.type === 'sibling') {
                    cleanTempChildWrap(drag);
                    var tGroup = t.targetGroupEl || drag.groupEl;
                    if (t.targetDepth !== undefined) {
                        drag.ghost.style.marginLeft = (t.targetDepth * 20) + 'px';
                    } else {
                        drag.ghost.style.marginLeft = drag.dragEl.style.marginLeft;
                    }
                    if (t.beforeEl) tGroup.insertBefore(drag.ghost, t.beforeEl);
                    else tGroup.appendChild(drag.ghost);
                    syncTagLastChild(tGroup, drag.dragEl);
                } else if (t.type === 'nest') {
                    drag.ghost.style.marginLeft = ((parseInt(t.parentEl.dataset.tagDepth) + 1) * 20) + 'px';
                    var cw = getTagChildWrap(t.parentEl.dataset.tagId);
                    if (!cw) {
                        cw = document.createElement('div'); cw.className = 'tag-children';
                        cw.dataset.parentId = t.parentEl.dataset.tagId;
                        cw.style.setProperty('--child-depth', parseInt(t.parentEl.dataset.tagDepth) + 1);
                        t.parentEl.parentNode.insertBefore(cw, t.parentEl.nextSibling);
                        drag.tempChildWrap = cw;
                    }
                    if (t.beforeEl) cw.insertBefore(drag.ghost, t.beforeEl);
                    else cw.appendChild(drag.ghost);
                    syncTagLastChild(cw, drag.dragEl);
                } else if (t.type === 'promote') {
                    cleanTempChildWrap(drag);
                    var parentGroup = drag.groupEl.parentNode;
                    var promoDepth = Math.max(0, (parseInt(drag.dragEl.dataset.tagDepth) || 1) - 1);
                    drag.ghost.style.marginLeft = (promoDepth * 20) + 'px';
                    if (t.beforeEl) parentGroup.insertBefore(drag.ghost, t.beforeEl);
                    else parentGroup.appendChild(drag.ghost);
                    syncTagLastChild(parentGroup, drag.dragEl);
                }
            }
        }
        drag.raf = requestAnimationFrame(dragLoop);
    }

    function cleanTempChildWrap(drag) {
        if (drag.tempChildWrap) {
            if (!drag.tempChildWrap.querySelector('.tag-item, .tag-ghost-placeholder'))
                drag.tempChildWrap.remove();
            drag.tempChildWrap = null;
        }
    }

    // Keep tag-last-child on the correct .tag-item in a group during drag.
    // The ghost (span) and absolute dragEl can both shift which element is truly last.
    function syncTagLastChild(groupEl, dragEl) {
        var items = groupEl.querySelectorAll(':scope > .tag-item');
        for (var i = 0; i < items.length; i++) {
            items[i].classList.remove('tag-last-child');
        }
        // If the ghost is the very last element child, no .tag-item should have tag-last-child
        var lastEl = groupEl.lastElementChild;
        if (lastEl && lastEl.classList.contains('tag-ghost-placeholder')) return;
        // Otherwise, assign to the last visible .tag-item
        for (var i = items.length - 1; i >= 0; i--) {
            if (items[i] !== dragEl && getComputedStyle(items[i]).position !== 'absolute') {
                items[i].classList.add('tag-last-child');
                break;
            }
        }
    }

    // Returns { type:'sibling'|'nest'|'promote', beforeEl:element|null, parentEl?:element }
    function computeDropTarget(drag) {
        var gr = drag.groupEl.getBoundingClientRect();
        var dr = drag.dragEl.getBoundingClientRect();
        // Use actual pointer position for hit tests, not the scaled element center
        var px = drag.pointerX - gr.left;
        var py = drag.pointerY - gr.top;
        var items = Array.from(drag.groupEl.querySelectorAll(':scope > .tag-item'));

        // --- nest onto tag's right half (with hover delay) ---
        // Runs BEFORE promote so deliberate nest targets take priority.
        // Left half = sibling reorder, right half = nest target.
        // Y uses hit padding; X uses actual tag bounds split at center.
        var now = Date.now();
        var nestTarget = null;
        var nestTargetEl = null;
        var siblingTargetEl = null;  // left-half target → become sibling of this tag
        var anyTagHit = false;
        var bestNestDist = Infinity;
        var bestSibDist = Infinity;
        var allTags = drag.container.querySelectorAll('.tag-item');
        // Absolute coordinates — independent of groupEl changes from promote
        var absPX = drag.pointerX;
        var absPY = drag.pointerY;
        for (var i = 0; i < allTags.length; i++) {
            if (allTags[i] === drag.dragEl) continue;
            var sr = allTags[i].getBoundingClientRect();
            if (absPY > sr.top - DRAG.hitPaddingY && absPY < sr.bottom + DRAG.hitPaddingY &&
                absPX > sr.left - 6 && absPX < sr.right + 6) {
                var sibId = allTags[i].dataset.tagId;
                if (sibId !== drag.dragTagId && !isDescendantOf(drag.dragTagId, sibId)) {
                    anyTagHit = true;
                    var absSX = (sr.left + sr.right) * 0.5;
                    var absSY = (sr.top + sr.bottom) * 0.5;
                    var dist = (absPX - absSX) * (absPX - absSX) + (absPY - absSY) * (absPY - absSY);
                    // Hysteresis: need 8px past center to switch left↔right, prevents jitter
                    var sideBias = 0;
                    if (drag._lastSide === 'right') sideBias = -8;
                    else if (drag._lastSide === 'left') sideBias = 8;
                    if (absPX >= absSX + sideBias) {
                        drag._lastSide = 'right';
                        if (dist < bestNestDist) { bestNestDist = dist; nestTarget = sibId; nestTargetEl = allTags[i]; }
                    } else {
                        drag._lastSide = 'left';
                        if (dist < bestSibDist) { bestSibDist = dist; siblingTargetEl = allTags[i]; }
                    }
                }
            }
        }
        if (nestTarget) {
            if (drag._nestHoverTarget !== nestTarget) {
                drag._nestHoverTarget = nestTarget;
                drag._nestHoverStart = now;
            }
            drag._nestHoverLeaveTime = 0;
            if (now - drag._nestHoverStart >= DRAG.hoverDelay) {
                // Y-sort among target's existing children — never blindly append to bottom
                var nestBefore = null;
                var childWrap = getTagChildWrap(nestTargetEl.dataset.tagId);
                if (childWrap) {
                    var cItems = Array.from(childWrap.querySelectorAll(':scope > .tag-item'));
                    if (cItems.length > 0) {
                        var cwRect = childWrap.getBoundingClientRect();
                        var cOthers = [];
                        for (var ci = 0; ci < cItems.length; ci++) {
                            var cr = cItems[ci].getBoundingClientRect();
                            cOthers.push({ el: cItems[ci], cy: cr.top + cr.height * 0.5 - cwRect.top });
                        }
                        cOthers.sort(function(a,b){return a.cy - b.cy;});
                        var cpy = absPY - cwRect.top;
                        for (var ck = 0; ck < cOthers.length; ck++) {
                            if (cpy < cOthers[ck].cy) { nestBefore = cOthers[ck].el; break; }
                        }
                    }
                }
                return { type: 'nest', parentEl: nestTargetEl, beforeEl: nestBefore };
            }
            // Timer running — snap ghost after hover target (same group only)
            var hoverIdx = items.indexOf(nestTargetEl);
            if (hoverIdx >= 0) {
                var nextItem = (hoverIdx < items.length - 1) ? items[hoverIdx + 1] : null;
                return { type: 'sibling', beforeEl: nextItem };
            }
            // Different group — fall through to sibling reorder
        } else if (siblingTargetEl) {
            // Left half — become sibling of target (move to its group, right after it)
            var sibGroup = siblingTargetEl.parentNode;
            var sibNext = siblingTargetEl.nextElementSibling;
            if (sibNext && sibNext.classList.contains('tag-children')) sibNext = sibNext.nextElementSibling;
            return { type: 'sibling', beforeEl: sibNext || null, targetGroupEl: sibGroup, targetDepth: parseInt(siblingTargetEl.dataset.tagDepth) || 0, siblingTargetEl: siblingTargetEl };
        } else if (drag._nestHoverTarget) {
            // Pointer left — 200ms grace period before unsnapping
            if (!drag._nestHoverLeaveTime) drag._nestHoverLeaveTime = now;
            if (now - drag._nestHoverLeaveTime < 200) {
                var lastEl = drag.container.querySelector('.tag-item[data-tag-id="' + drag._nestHoverTarget + '"]');
                if (lastEl) {
                    var hIdx = items.indexOf(lastEl);
                    if (hIdx >= 0) {
                        var nItem = (hIdx < items.length - 1) ? items[hIdx + 1] : null;
                        return { type: 'sibling', beforeEl: nItem };
                    }
                }
            }
            drag._nestHoverTarget = null;
            drag._nestHoverStart = 0;
            drag._nestHoverLeaveTime = 0;
        }
        // --- promote? (200ms delay so it doesn't fight with sibling-target at edges) ---
        if (!anyTagHit && drag.parentId && drag.groupEl.classList.contains('tag-children')
            && (drag.startPointerX - drag.pointerX) > 40) {
            if (!drag._promoteEnterTime) drag._promoteEnterTime = now;
            if (now - drag._promoteEnterTime >= 200) {
                var pg = drag.groupEl.parentNode;
                var parentTag = pg.querySelector('.tag-item[data-tag-id="' + drag.parentId + '"]');
                if (parentTag) {
                    var sibs = Array.from(pg.querySelectorAll(':scope > .tag-item'));
                    var sibOthers = [];
                    for (var si = 0; si < sibs.length; si++) {
                        if (sibs[si] === drag.dragEl) continue;
                        var pr = sibs[si].getBoundingClientRect();
                        sibOthers.push({ el: sibs[si], cy: pr.top + pr.height * 0.5 - pg.getBoundingClientRect().top });
                    }
                    sibOthers.sort(function(a,b){return a.cy - b.cy;});
                    var pgRect = pg.getBoundingClientRect();
                    var pdcy = dr.top + dr.height * 0.5 - pgRect.top;
                    var beforeInPg = null;
                    for (var sj = 0; sj < sibOthers.length; sj++) {
                        if (pdcy < sibOthers[sj].cy) { beforeInPg = sibOthers[sj].el; break; }
                    }
                    return { type: 'promote', beforeEl: beforeInPg };
                }
            }
        } else {
            drag._promoteEnterTime = 0;
        }
        // --- default: sibling reorder (pure Y-sort, like folder drag) ---
        var others = [];
        for (var j = 0; j < items.length; j++) {
            if (items[j] === drag.dragEl) continue;
            var ir = items[j].getBoundingClientRect();
            others.push({ el: items[j], cy: ir.top + ir.height * 0.5 - gr.top });
        }
        others.sort(function(a, b) { return a.cy - b.cy; });
        for (var k = 0; k < others.length; k++) {
            if (py < others[k].cy) return { type: 'sibling', beforeEl: others[k].el };
        }
        return { type: 'sibling', beforeEl: null };
    }

    function onTagPointerUp(e) {
        document.removeEventListener('pointermove', onTagPointerMove);
        document.removeEventListener('pointerup', onTagPointerUp);
        document.removeEventListener('pointercancel', onTagPointerUp);
        if (!tagDrag || !tagDrag.active) return;
        var drag = tagDrag;
        cancelAnimationFrame(drag.raf);
        drag.active = false;
        if (drag.groupEl) drag.groupEl.style.touchAction = '';

        var t = drag.didMove ? computeDropTarget(drag) : null;
        if (t) {
            var tag = tags.find(function(tg) { return tg.id === drag.dragTagId; });
            if (tag) {
                var dxEnd = drag.pointerX - drag.startPointerX;
                var dyEnd = drag.pointerY - drag.startPointerY;
                var dNowEnd = t.parentEl ? (parseInt(t.parentEl.dataset.tagDepth)+1) : 0;
                console.log('[DROP]', drag.dragTagId.slice(-8),
                    'finalΔ('+dxEnd.toFixed(0)+','+dyEnd.toFixed(0)+')',
                    'L'+drag.startDepth+'→L'+dNowEnd,
                    t.type,
                    'parent='+(t.parentEl?t.parentEl.dataset.tagId.slice(-8):'ROOT'),
                    'spid='+(t.parentEl?tagTree.querySelector('.tag-item[data-tag-id="'+t.parentEl.dataset.tagId+'"]')?'found':'MISSING':'n/a'));
                if (t.type === 'nest') {
                    var newPid = t.parentEl ? t.parentEl.dataset.tagId : null;
                    if (newPid && newPid !== drag.dragTagId && !isDescendantOf(drag.dragTagId, newPid)
                        && tag.parentId !== newPid) {
                        tag.parentId = newPid;
                        Storage.updateTag(tag).catch(function(err) { console.warn(err); });
                    }
                } else if (t.type === 'promote') {
                    var pg2 = drag.groupEl.parentNode;
                    var pw = pg2.closest('.tag-children');
                    var newPid2 = pw ? (pw.dataset.parentId || null) : null;
                    if (tag.parentId !== (newPid2 || null)) {
                        tag.parentId = newPid2 || null;
                        Storage.updateTag(tag).catch(function(err) { console.warn(err); });
                    }
                } else if (t.siblingTargetEl) {
                    // Left-half drop: become sibling of target (same parent)
                    var sibParent = t.siblingTargetEl.parentNode;
                    var sibWrapper = sibParent.classList.contains('tag-children') ? sibParent : null;
                    var newPid3 = sibWrapper ? (sibWrapper.dataset.parentId || null) : null;
                    if (tag.parentId !== (newPid3 || null)) {
                        tag.parentId = newPid3 || null;
                        Storage.updateTag(tag).catch(function(err) { console.warn(err); });
                    }
                }
                // reorder in flat array
                var oldIdx = tags.findIndex(function(tg) { return tg.id === drag.dragTagId; });
                if (oldIdx !== -1) {
                    var moved = tags.splice(oldIdx, 1)[0];
                    if (t.beforeEl) {
                        var refIdx = tags.findIndex(function(tg) { return tg.id === t.beforeEl.dataset.tagId; });
                        tags.splice(refIdx !== -1 ? refIdx : tags.length, 0, moved);
                    } else if (t.type === 'promote') {
                        var formerParent = tags.find(function(tg) { return tg.id === drag.parentId; });
                        if (formerParent) {
                            var fpIdx = tags.findIndex(function(tg) { return tg.id === formerParent.id; });
                            tags.splice(fpIdx + 1, 0, moved);
                        } else tags.push(moved);
                    } else if (t.type === 'nest' && t.parentEl) {
                        var pi = tags.findIndex(function(tg) { return tg.id === t.parentEl.dataset.tagId; });
                        tags.splice(pi + 1, 0, moved);
                    } else {
                        // Sibling reorder to end: insert after last sibling, not global end
                        var movedParent = moved.parentId || null;
                        var lastSibIdx = -1;
                        for (var si = tags.length - 1; si >= 0; si--) {
                            if ((tags[si].parentId || null) === movedParent) {
                                lastSibIdx = si; break;
                            }
                        }
                        tags.splice(lastSibIdx >= 0 ? lastSibIdx + 1 : tags.length, 0, moved);
                    }
                }
                saveTagOrder();
            }
        }

        if (drag.ghost && drag.ghost.parentNode) drag.ghost.parentNode.removeChild(drag.ghost);
        drag.ghost = null;
        drag.dragEl.style.position = '';
        drag.dragEl.style.left = '';
        drag.dragEl.style.top = '';
        drag.dragEl.style.zIndex = '';
        drag.dragEl.style.pointerEvents = '';
        drag.dragEl.style.scale = '';
        drag.dragEl.style.boxShadow = '';
        drag.dragEl.style.transition = '';
        drag.dragEl.style.transform = '';
        cleanTempChildWrap(drag);
        if (drag.collapsedChildWrap) {
            drag.collapsedChildWrap.style.display = '';
            drag.collapsedChildWrap = null;
        }
        drag.container.classList.remove('is-dragging');
        tagDrag = null;
        renderTagTree();
    }
    // ★ 标签排序存储格式：{ __root__: [id1, id2], parentIdA: [id3, id4], ... }
    async function saveTagOrder() {
        try {
            const orderMap = {};
            // 根级标签顺序
            const roots = tags.filter(t => !t.parentId).map(t => t.id);
            if (roots.length) orderMap['__root__'] = roots;
            // 每个父标签下的子标签顺序
            const parentIds = new Set(tags.filter(t => t.parentId).map(t => t.parentId));
            for (const pid of parentIds) {
                const children = tags.filter(t => t.parentId === pid).map(t => t.id);
                if (children.length) orderMap[pid] = children;
            }
            if (typeof Storage !== 'undefined' && Storage.setSetting) {
                await Storage.setSetting('tag_order', orderMap);
            }
        } catch (err) {
            console.warn('[Sidebar] 保存标签排序失败:', err.message);
        }
    }

    async function loadTagOrder() {
        try {
            if (typeof Storage !== 'undefined' && Storage.getSetting) {
                const orderData = await Storage.getSetting('tag_order', {});
                if (orderData && typeof orderData === 'object' && !Array.isArray(orderData)) {
                    // 新格式：{ __root__: [...], parentId: [...] }
                    const allOrdered = [];
                    const seen = new Set();
                    function addGroup(parentKey) {
                        const ids = orderData[parentKey] || [];
                        for (const id of ids) {
                            if (!seen.has(id) && tags.some(t => t.id === id)) {
                                allOrdered.push(id);
                                seen.add(id);
                                // 递归添加子标签
                                addGroup(id);
                            }
                        }
                    }
                    // 先加根级
                    addGroup('__root__');
                    // 再加没有出现在 order 中的孤儿标签
                    for (const t of tags) {
                        if (!seen.has(t.id)) {
                            allOrdered.push(t.id);
                            seen.add(t.id);
                            addGroup(t.id);
                        }
                    }
                    const orderMap = new Map(allOrdered.map((id, i) => [id, i]));
                    tags.sort((a, b) => {
                        const ai = orderMap.get(a.id);
                        const bi = orderMap.get(b.id);
                        if (ai !== undefined && bi !== undefined) return ai - bi;
                        if (ai !== undefined) return -1;
                        if (bi !== undefined) return 1;
                        return 0;
                    });
                } else if (Array.isArray(orderData) && orderData.length > 0) {
                    // 兼容旧格式：扁平数组
                    const orderMap = new Map(orderData.map((id, i) => [id, i]));
                    tags.sort((a, b) => {
                        const ai = orderMap.get(a.id);
                        const bi = orderMap.get(b.id);
                        if (ai !== undefined && bi !== undefined) return ai - bi;
                        if (ai !== undefined) return -1;
                        if (bi !== undefined) return 1;
                        return 0;
                    });
                }
            }
        } catch (err) {
            console.warn('[Sidebar] 加载标签排序失败:', err.message);
        }
    }

    /**
     * 显示删除标签确认对话框
     */
    async function showDeleteTagDialog(tag) {
        const overlay = document.getElementById('modalOverlay');
        const content = document.getElementById('modalContent');

        // 获取关联的图片数量
        let imageCount = 0;
        try {
            const imagePaths = await Storage.getImagesForTag(tag.id);
            imageCount = imagePaths.length;
        } catch (e) { /* ignore */ }

        // 检查子标签
        const childIds = getTagDescendantIds(tag.id);
        const childCount = childIds.length;

        content.innerHTML = `
            <h2><span class="icon icon-delete"></span> ${t("sidebar.delete_tag")}</h2>
            <p style="color: var(--text-secondary); margin-bottom: 12px;">
                ${t("sidebar.confirm_delete_tag_detail").replace('{name}', '<strong style="color: var(--text-primary);">「' + escapeHtmlSidebar(tag.name) + '」</strong>')}
            </p>
            ${childCount > 0 ? `<p class="tag-delete-warning"><span class="icon icon-warning"></span> ${t("sidebar.delete_tag_child_warn").replace('{n}', childCount)}</p>` : ''}
            ${imageCount > 0 ? `<p class="tag-delete-warning"><span class="icon icon-warning"></span> ${t("sidebar.delete_tag_image_warn").replace('{n}', imageCount)}</p>` : ''}
            <p style="font-size: 11px; color: var(--text-muted);">${t("sidebar.cannot_undo")}</p>
            <div class="modal-actions">
                <button id="btnCancelDeleteTag" class="btn-secondary">${t("sidebar.cancel")}</button>
                <button id="btnConfirmDeleteTag" class="btn-primary" style="background: #e94560; border-color: #e94560;">${t("sidebar.confirm_delete")}</button>
            </div>
        `;

        overlay.style.display = 'flex';

        document.getElementById('btnCancelDeleteTag').addEventListener('click', () => {
            overlay.style.display = 'none';
        });

        document.getElementById('btnConfirmDeleteTag').addEventListener('click', async () => {
            overlay.style.display = 'none';
            try {
                await Storage.deleteTag(tag.id);
                await refreshTagTree();
                App.showToast(t('toast.tag_deleted_with_name').replace('{name}', tag.name), 'success');
            } catch (err) {
                App.showToast(t('toast.delete_failed') + ': ' + err.message, 'error');
            }
        });

        overlay.addEventListener('click', (e) => {
            if (e.target === overlay) overlay.style.display = 'none';
        });
    }

    /**
     * 显示新建标签对话框（v2 — 丰富样式选项）
     */
    function showNewTagDialog() {
        const overlay = document.getElementById('modalOverlay');
        const content = document.getElementById('modalContent');

        // ============ 样式标签默认值 ============
        let iconOnly = false;
        let iconOnlySize = 16;
        let selectedFillStyle = 'solid';
        let selectedFillOpacity = 100;
        let selectedShape = 'soft';
        let selectedIcon = '';
        let selectedBorderStyle = 'solid';
        let selectedBorderOpacity = 100;
        let selectedSize = 'md';
        let selectedTextSize = 14;
        let selectedWeight = 'regular';
        let styleBgColor = '#9b59b6';
        let styleTextColor = '#ffffff';

        // ============ 头像标签状态 ============
        let avatarDataUrl = null;
        let avatarCropBlob = null;   // 裁剪后的 Blob，用于保存到文件
        let avatarBgColor = '#2a2a2a';
        let avatarTextColor = '#ffffff';
        let avatarShape = 'round';
        let avatarImageOnly = false;
        let avatarThumbSize = 40;
        let avatarTextSize = 12;
        let avatarCropFile = null;
        let avatarCropImgNatW = 0, avatarCropImgNatH = 0;
        let avatarCropState = { x: 0, y: 0, scale: 1 };
        let avatarCropIsDrag = false;
        let avatarCropDragSX = 0, avatarCropDragSY = 0, avatarCropDragOX = 0, avatarCropDragOY = 0;

        function closeDialog() { content.classList.remove('wide'); overlay.style.display = 'none'; }


        content.innerHTML = `
            <h2><span class="icon icon-tag"></span> ${t("sidebar.new_tag")}</h2>
            <div class="new-tag-columns">
                <div class="new-tag-column">
                    <h3><span class="icon icon-art"></span> ${t("sidebar.style_tag")}</h3>
                    <div class="modal-actions" style="margin-top:0;margin-bottom:16px;">
                        <button class="btn-secondary btn-cancel-dialog">${t("sidebar.cancel")}</button>
                        <button id="btnCreateStyleTag" class="btn-primary">${t("sidebar.confirm")}</button>
                    </div>
                    <div class="form-group">
                        <label>${t("sidebar.tag_name")} <span style="color:var(--text-muted);font-weight:400;">${t("sidebar.icon_optional")}</span></label>
                        <div class="tag-name-row">
                            <input type="text" id="newTagName" placeholder="${t("sidebar.enter_name")}" style="flex:1;" />
                            <label class="tag-name-show-cb"><input type="checkbox" id="newTagShowName" checked /></label>
                        </div>
                    </div>
                    <div class="form-group">
                        <label>${t("sidebar.parent_tag_optional")}</label>
                        <select id="newTagParent" style="width:100%;padding:6px 8px;background:var(--bg-input);border:1px solid var(--border-color);border-radius:var(--radius-sm);color:var(--text-primary);">
                            <option value="">${t("sidebar.no_parent_root")}</option>
                        </select>
                    </div>
                    <div class="tag-style-section">
                        <label>${t("sidebar.bg_color")}</label>
                        <div class="avatar-shadow-row">
                            <input type="color" id="styleBgColor" value="${styleBgColor}" />
                        </div>
                    </div>
                    <div class="tag-style-section">
                        <label>${t("sidebar.text_color")}</label>
                        <div class="avatar-shadow-row">
                            <input type="color" id="styleTextColor" value="${styleTextColor}" />
                        </div>
                    </div>
                    <div class="tag-style-section">
                        <label>${t("sidebar.fill_style")}</label>
                        <div class="tag-option-row" id="tagFillStyle">
                            <button class="tag-option-btn${selectedFillStyle==='solid'?' selected':''}" data-value="solid">${t("sidebar.fill_solid")}</button>
                            <button class="tag-option-btn${selectedFillStyle==='outline'?' selected':''}" data-value="outline">${t("sidebar.fill_outline")}</button>
                        </div>
                        <div class="tag-opacity-row">
                            <label class="tag-opacity-label">${t("sidebar.opacity")}</label>
                            <input type="range" id="tagFillOpacity" min="0" max="100" value="${selectedFillOpacity}" step="5" />
                            <span class="tag-opacity-value">${selectedFillOpacity}%</span>
                        </div>
                    </div>
                    <div class="tag-style-section">
                        <label>${t("sidebar.shape")}</label>
                        <div class="tag-option-row" id="tagShape">
                            <button class="tag-option-btn${selectedShape==='sharp'?' selected':''}" data-value="sharp">${t("sidebar.shape_sharp")}</button>
                            <button class="tag-option-btn${selectedShape==='soft'?' selected':''}" data-value="soft">${t("sidebar.shape_soft")}</button>
                            <button class="tag-option-btn${selectedShape==='pill'?' selected':''}" data-value="pill">${t("sidebar.shape_pill")}</button>
                            <button class="tag-option-btn${selectedShape==='leftbar'?' selected':''}" data-value="leftbar">${t("sidebar.shape_leftbar")}</button>
                        </div>
                    </div>
                    <div class="tag-style-section">
                        <label>${t("sidebar.prefix_icon")}</label>
                        <div class="tag-icon-input-row">
                            <input type="text" id="tagIconInput" value="${escapeHtmlSidebar(selectedIcon.indexOf('data:')===0?'${t("sidebar.custom_icon")}':selectedIcon)}" placeholder="${t("sidebar.icon_input_hint")}" maxlength="4" ${selectedIcon.indexOf('data:')===0?'readonly':''} />
                            <button class="btn-small" id="btnIconPicker" type="button">${t("sidebar.select_icon")}</button>
                            <button class="btn-small" id="btnIconFile" type="button" title="${t("sidebar.icon_from_file")}">📁</button>
                        </div>
                        <div class="tag-icon-picker" id="tagIconPicker" style="display:none;">${buildIconPickerHTML()}</div>
                    </div>
                    <div class="tag-style-section">
                        <label>${t("sidebar.border")}</label>
                        <div class="tag-option-row" id="tagBorderStyle">
                            <button class="tag-option-btn${selectedBorderStyle==='solid'?' selected':''}" data-value="solid">${t("sidebar.border_solid")}</button>
                            <button class="tag-option-btn${selectedBorderStyle==='dashed'?' selected':''}" data-value="dashed">${t("sidebar.border_dashed")}</button>
                            <button class="tag-option-btn${selectedBorderStyle==='none'?' selected':''}" data-value="none">${t("sidebar.border_none")}</button>
                        </div>
                        <div class="tag-opacity-row">
                            <label class="tag-opacity-label">${t("sidebar.border")}${t("sidebar.opacity")}</label>
                            <input type="range" id="tagBorderOpacity" min="0" max="100" value="100" step="5" />
                            <span class="tag-opacity-value" id="tagBorderOpacityVal">100%</span>
                        </div>
                    </div>
                    <div class="tag-style-section">
                        <label>尺寸</label>
                        <div class="tag-option-row" id="tagSize">
                            <button class="tag-option-btn${selectedSize==='sm'?' selected':''}" data-value="sm">${t("sidebar.size_small")}</button>
                            <button class="tag-option-btn${selectedSize==='md'?' selected':''}" data-value="md">${t("sidebar.size_default")}</button>
                            <button class="tag-option-btn${selectedSize==='lg'?' selected':''}" data-value="lg">${t("sidebar.size_large")}</button>
                        </div>
                        <div class="tag-opacity-row" style="margin-top:8px;">
                            <label class="tag-opacity-label">${t("sidebar.font_size")}</label>
                            <input type="range" id="newTextSizeSlider" min="10" max="60" value="${selectedTextSize}" step="1" />
                            <span class="tag-opacity-value" id="newTextSizeVal">${selectedTextSize}px</span>
                        </div>
                        <div class="tag-opacity-row" style="margin-top:8px;">
                            <label class="tag-opacity-label">${t("sidebar.icon_size")}</label>
                            <input type="range" id="newIconOnlySizeSlider" min="12" max="300" value="16" step="1">
                            <span class="tag-opacity-value" id="newIconOnlySizeVal">16px</span>
                        </div>
                    </div>
                    <div class="tag-style-section">
                        <label>${t("sidebar.font_weight")}</label>
                        <div class="tag-option-row" id="tagWeight">
                            <button class="tag-option-btn${selectedWeight==='regular'?' selected':''}" data-value="regular">${t("sidebar.weight_regular")}</button>
                            <button class="tag-option-btn${selectedWeight==='bold'?' selected':''}" data-value="bold">${t("sidebar.weight_bold")}</button>
                        </div>
                    </div>
                    <div class="tag-preview-container">
                        <span id="tagPreview" class="tag-badge tag-fill-solid tag-shape-pill tag-size-md tag-weight-regular" style="background:#9b59b6;color:#fff;border:1.5px solid #9b59b6;"></span>
                    </div>
                </div>
                <div class="new-tag-divider"></div>
                <div class="new-tag-column">
                    <h3><span class="icon icon-user"></span> ${t("sidebar.avatar_tag")}</h3>
                    <div class="modal-actions" style="margin-top:0;margin-bottom:16px;">
                        <button class="btn-secondary btn-cancel-dialog">${t("sidebar.cancel")}</button>
                        <button id="btnCreateAvatarTag" class="btn-primary">${t("sidebar.confirm")}</button>
                    </div>
                    <div class="avatar-row">
                        <div class="avatar-upload-area" id="avatarUploadArea" title="${t("sidebar.click_upload_avatar")}">
                            <span class="avatar-upload-placeholder"><span class="icon icon-image"></span></span>
                            <div class="avatar-upload-cam"><span class="icon icon-image"></span></div>
                        </div>
                        <div class="avatar-info">
                            <div class="form-group">
                                <label>${t("sidebar.tag_name")} <span style="color:var(--text-muted);font-weight:400;">${t("sidebar.icon_optional")}</span></label>
                                <div class="tag-name-row">
                                    <input type="text" id="avatarTagName" placeholder="${t("sidebar.enter_name")}" style="flex:1;" />
                                    <label class="tag-name-show-cb"><input type="checkbox" id="avatarTagShowName" checked /></label>
                                </div>
                            </div>
                            <div class="avatar-shadow-row">
                                <label style="font-size:11px;color:var(--text-secondary);">${t("sidebar.bg_color")}</label>
                                <input type="color" id="avatarBgColor" value="${avatarBgColor}" />
                            </div>
                            <div class="avatar-shadow-row">
                                <label style="font-size:11px;color:var(--text-secondary);">${t("sidebar.text_color")}</label>
                                <input type="color" id="avatarTextColor" value="${avatarTextColor}" />
                            </div>
                            <div class="tag-style-section" style="margin-top:8px;">
                                <label>${t("sidebar.shape")}</label>
                                <div class="tag-option-row" id="tagAvatarShape">
                                    <button class="tag-option-btn${avatarShape==='sharp'?' selected':''}" data-value="sharp">${t("sidebar.shape_sharp")}</button>
                                    <button class="tag-option-btn${avatarShape==='soft'?' selected':''}" data-value="soft">${t("sidebar.shape_soft")}</button>
                                    <button class="tag-option-btn${avatarShape==='round'?' selected':''}" data-value="round">${t("sidebar.shape_round")}</button>
                                </div>
                                <div class="tag-opacity-row" style="margin-top:8px;">
                                    <label class="tag-opacity-label">${t("sidebar.avatar_thumb_size")}</label>
                                    <input type="range" id="newAvatarThumbSizeSlider" min="24" max="300" value="40" step="2" />
                                    <span class="tag-opacity-value" id="newAvatarThumbSizeVal">40px</span>
                                </div>
                                <div class="tag-opacity-row" style="margin-top:8px;">
                                    <label class="tag-opacity-label">${t("sidebar.avatar_font_size")}</label>
                                    <input type="range" id="newAvatarFontSizeSlider" min="8" max="60" value="12" step="1" />
                                    <span class="tag-opacity-value" id="newAvatarFontSizeVal">12px</span>
                                </div>
                            </div>
                        </div>
                    </div>
                    <div class="tag-preview-container" style="margin-top:10px;">
                        <div class="avatar-preview-tag" id="avatarPreview" style="border-radius:14px;">
                            <div class="av-thumb" style="width:40px;height:40px;border-radius:10px 0 0 10px;background:var(--bg-hover);display:flex;align-items:center;justify-content:center;font-size:16px;"><span class="icon icon-image"></span></div>
                            <span class="av-label" style="background:${avatarBgColor};color:${avatarTextColor};border-radius:0 14px 14px 0;">${t("sidebar.preview")}</span>
                        </div>
                    </div>
                </div>
                <div class="new-tag-divider"></div>
                <div class="new-tag-column">
                    <h3><span class="icon icon-art"></span> ${t("sidebar.html_tag")}</h3>
                    <div class="modal-actions" style="margin-top:0;margin-bottom:16px;">
                        <button class="btn-secondary btn-cancel-dialog">${t("sidebar.cancel")}</button>
                        <button id="btnCreateHtmlTag" class="btn-primary">${t("sidebar.confirm")}</button>
                    </div>
                    <div class="form-group">
                        <label>${t("sidebar.tag_name")}</label>
                        <div class="tag-name-row">
                            <input type="text" id="htmlTagName" placeholder="${t("sidebar.enter_name")}" style="flex:1;" />
                            <label class="tag-name-show-cb"><input type="checkbox" id="htmlTagShowName" checked /></label>
                        </div>
                    </div>
                    <div class="form-group">
                        <label>${t("sidebar.parent_tag_optional")}</label>
                        <select id="htmlTagParent" style="width:100%;"><option value="">${t("sidebar.no_parent_root")}</option></select>
                    </div>
                    <div class="form-group">
                        <label>${t("sidebar.tag_width")}</label>
                        <div style="display:flex;align-items:center;gap:8px;">
                            <input type="number" id="htmlTagWidth" value="120" min="10" max="800" step="1" style="width:100px;" />
                            <span style="font-size:12px;color:var(--text-muted);">px</span>
                            <span style="font-size:12px;color:var(--text-muted);">${t("sidebar.tag_height")}</span>
                            <input type="number" id="htmlTagHeight" value="40" min="10" max="800" step="1" style="width:100px;" />
                            <span style="font-size:12px;color:var(--text-muted);">px</span>
                        </div>
                    </div>
                    <div class="form-group">
                        <label>${t("sidebar.html_css_code")}</label>
                        <textarea id="htmlTagCode" class="html-tag-code-editor" placeholder="&lt;style&gt;&#10;.my-tag {&#10;  background: linear-gradient(135deg, #667eea 0%, #764ba2 100%);&#10;  color: #fff;&#10;  border-radius: 20px;&#10;  padding: 8px 16px;&#10;  font-size: 14px;&#10;  text-shadow: 0 1px 3px rgba(0,0,0,0.3);&#10;}&#10;&lt;/style&gt;&#10;&lt;div class=&quot;my-tag&quot;&gt;标签文字&lt;/div&gt;" rows="12"></textarea>
                    </div>
                    <div class="form-group">
                        <label>${t("sidebar.preview")}</label>
                        <div class="html-tag-preview-box" id="htmlTagPreviewBox" style="min-height:50px;border:1px dashed var(--border-color);border-radius:4px;padding:6px;display:flex;align-items:center;justify-content:center;overflow:hidden;"></div>
                    </div>
                </div>
            </div>
            <div class="preset-colors-section">
                <label>${t("sidebar.preset_palette")}</label>
                <div class="preset-colors-grid" id="presetColorsGrid"></div>
            </div>
            <input type="file" id="avatarFileInput" accept="image/*" style="display:none;" />
        `;

        content.classList.add('wide');
        overlay.style.display = 'flex';

        // ============ 填充父标签下拉 ============
        const parentSelect = document.getElementById('newTagParent');
        if (parentSelect && tags.length > 0) {
            function buildParentOptions(tagList, indent = 0) {
                let html = '';
                for (const t of tagList) {
                    html += `<option value="${t.id}">${'　'.repeat(indent)}${indent > 0 ? '└ ' : ''}${escapeHtmlSidebar(t.name)}</option>`;
                    // 添加直接子标签
                    const children = tags.filter(c => c.parentId === t.id);
                    if (children.length) {
                        html += buildParentOptions(children, indent + 1);
                    }
                }
                return html;
            }
            parentSelect.innerHTML = `<option value="">${t("sidebar.no_parent_root")}</option>` + buildParentOptions(tags.filter(t => !t.parentId));
        }

        // ============ 预设色板 ============
        const DEFAULT_PRESETS = [
            '#ff0000','#ff4500','#ff8c00','#ffd700','#ffff00','#adff2f','#00ff00','#00ced1','#1e90ff','#0000ff',
            '#8a2be2','#ff00ff','#ff1493','#dc143c','#c0c0c0','#808080','#ffffff','#000000','#8b4513','#f5deb3'
        ];
        let colorPresets = [];
        try {
            const saved = localStorage.getItem('tagColorPresets');
            colorPresets = saved ? JSON.parse(saved) : [...DEFAULT_PRESETS];
        } catch (e) {
            colorPresets = [...DEFAULT_PRESETS];
        }
        let lastFocusedColorInput = null;

        function savePresets() {
            saveTagColorPresetsToServer(colorPresets);
        }

        function renderPresetGrid() {
            const grid = document.getElementById('presetColorsGrid');
            if (!grid) return;
            grid.innerHTML = colorPresets.map((c, i) =>
                `<div class="preset-color-swatch" data-index="${i}" style="background:${c};" title="${c} — ${t("sidebar.left_click_pick_right_click_save")}"></div>`
            ).join('');
            grid.querySelectorAll('.preset-color-swatch').forEach(swatch => {
                swatch.addEventListener('click', (e) => {
                    if (!lastFocusedColorInput) return;
                    const color = colorPresets[parseInt(swatch.dataset.index)];
                    lastFocusedColorInput.value = color;
                    lastFocusedColorInput.dispatchEvent(new Event('input', { bubbles: true }));
                });
                swatch.addEventListener('contextmenu', (e) => {
                    e.preventDefault();
                    if (!lastFocusedColorInput) return;
                    const idx = parseInt(swatch.dataset.index);
                    colorPresets[idx] = lastFocusedColorInput.value;
                    savePresets();
                    swatch.style.background = lastFocusedColorInput.value;
                    swatch.classList.remove('saved');
                    void swatch.offsetWidth;
                    swatch.classList.add('saved');
                });
            });
        }
        renderPresetGrid();

        // Track last focused color input
        content.querySelectorAll('input[type="color"]').forEach(input => {
            input.addEventListener('focus', () => { lastFocusedColorInput = input; });
        });
        // Default to first color input
        const firstColorInput = content.querySelector('input[type="color"]');
        if (firstColorInput) lastFocusedColorInput = firstColorInput;

        // ============ 样式标签侧逻辑 ============
        const stylePreview = document.getElementById('tagPreview');
        const nameInput = document.getElementById('newTagName');

        function updateStylePreview() {
            const name = nameInput.value.trim() || t('sidebar.tag_preview');
            const isIconOnly = selectedIcon && !name;
            const showName = document.getElementById('newTagShowName')?.checked !== false;
            if (isIconOnly || !showName) {
                let iconHtml = '';
                if (selectedIcon) {
                    const sz = iconOnlySize + 'px';
                    if (selectedIcon.indexOf('data:') === 0) {
                        iconHtml = '<img src="' + selectedIcon + '" style="width:' + sz + ';height:' + sz + ';object-fit:contain;" alt="" />';
                    } else {
                        iconHtml = '<span style="font-size:' + sz + ';line-height:1;">' + selectedIcon + '</span>';
                    }
                }
                stylePreview.innerHTML = iconHtml;
                stylePreview.className = 'tag-badge';
                stylePreview.removeAttribute('style');
                TagStyle.apply(stylePreview, {
                    color: styleBgColor, fillStyle: selectedFillStyle, fillOpacity: selectedFillOpacity, shape: selectedShape,
                    icon: selectedIcon, borderStyle: selectedBorderStyle, borderOpacity: selectedBorderOpacity, size: selectedSize, textSize: selectedTextSize, weight: selectedWeight,
                    bgColor: styleBgColor, textColor: styleTextColor
                });
            } else {
                let iconHtml = '';
                if (selectedIcon) {
                    const sz = iconOnlySize + 'px';
                    if (selectedIcon.indexOf('data:') === 0) {
                        iconHtml = '<img src="' + selectedIcon + '" class="tag-icon-img" style="width:' + sz + ' !important;height:' + sz + ' !important;" alt="" />';
                    } else {
                        iconHtml = '<span style="font-size:' + sz + ';line-height:1;">' + selectedIcon + '</span>';
                    }
                }
                stylePreview.innerHTML = iconHtml + '<span>' + escapeHtmlSidebar(name) + '</span>';
                stylePreview.className = 'tag-badge';
                stylePreview.removeAttribute('style');
                TagStyle.apply(stylePreview, {
                    color: styleBgColor, fillStyle: selectedFillStyle, fillOpacity: selectedFillOpacity, shape: selectedShape,
                    icon: selectedIcon, borderStyle: selectedBorderStyle, borderOpacity: selectedBorderOpacity, size: selectedSize, textSize: selectedTextSize, weight: selectedWeight,
                    bgColor: styleBgColor, textColor: styleTextColor
                });
            }
        }
        updateStylePreview();
        nameInput.addEventListener('input', updateStylePreview);
        const showNameCb = document.getElementById('newTagShowName');
        if (showNameCb) showNameCb.addEventListener('change', updateStylePreview);

        function bindOptionRow(rowId, setter) {
            const row = document.getElementById(rowId);
            if (!row) return;
            row.querySelectorAll('.tag-option-btn').forEach(btn => {
                btn.addEventListener('click', () => {
                    row.querySelectorAll('.tag-option-btn').forEach(b => b.classList.remove('selected'));
                    btn.classList.add('selected');
                    setter(btn.dataset.value);
                    updateStylePreview();
                });
            });
        }
        bindOptionRow('tagFillStyle', v => { selectedFillStyle = v; });
        bindOptionRow('tagShape', v => { selectedShape = v; });
        bindOptionRow('tagBorderStyle', v => { selectedBorderStyle = v; });
        bindOptionRow('tagSize', v => { selectedSize = v; });
        bindOptionRow('tagWeight', v => { selectedWeight = v; });

        // Icon size slider (always visible)
        var iconOnlySizeSlider = document.getElementById('newIconOnlySizeSlider');
        var iconOnlySizeVal = document.getElementById('newIconOnlySizeVal');
        if (iconOnlySizeSlider) {
            iconOnlySizeSlider.addEventListener('input', function() {
                iconOnlySize = parseInt(this.value);
                if (iconOnlySizeVal) iconOnlySizeVal.textContent = iconOnlySize + 'px';
                updateStylePreview();
            });
        }

        // Text size slider for new tag
        var textSizeSlider = document.getElementById('newTextSizeSlider');
        var textSizeVal = document.getElementById('newTextSizeVal');
        if (textSizeSlider) {
            textSizeSlider.addEventListener('input', function() {
                selectedTextSize = parseInt(this.value);
                if (textSizeVal) textSizeVal.textContent = selectedTextSize + 'px';
                updateStylePreview();
            });
        }

        // Fill opacity slider
        var fillOpacitySlider = document.getElementById('tagFillOpacity');
        var fillOpacityVal = document.querySelector('.tag-opacity-value');
        if (fillOpacitySlider) {
            fillOpacitySlider.addEventListener('input', function() {
                selectedFillOpacity = parseInt(this.value);
                if (fillOpacityVal) fillOpacityVal.textContent = selectedFillOpacity + '%';
                updateStylePreview();
            });
        }

        // Border opacity slider
        var borderOpacitySlider = document.getElementById('tagBorderOpacity');
        var borderOpacityVal = document.getElementById('tagBorderOpacityVal');
        if (borderOpacitySlider) {
            borderOpacitySlider.addEventListener('input', function() {
                selectedBorderOpacity = parseInt(this.value);
                if (borderOpacityVal) borderOpacityVal.textContent = this.value + '%';
                updateStylePreview();
            });
        }

        // Icon picker
        var iconInput = document.getElementById('tagIconInput');
        var iconPicker = document.getElementById('tagIconPicker');
        document.getElementById('btnIconPicker').addEventListener('click', function(e) {
            e.stopPropagation();
            iconPicker.style.display = iconPicker.style.display === 'none' ? 'block' : 'none';
        });
        iconPicker.querySelectorAll('.tag-icon-option').forEach(function(opt) {
            opt.addEventListener('click', function() {
                iconInput.value = opt.dataset.icon;
                selectedIcon = opt.dataset.icon;
                iconInput.removeAttribute('readonly');
                iconPicker.querySelectorAll('.tag-icon-option').forEach(function(o) { o.classList.remove('selected'); });
                opt.classList.add('selected');
                iconPicker.style.display = 'none';
                updateStylePreview();
            });
        });
        iconInput.addEventListener('input', function() {
            selectedIcon = iconInput.value;
            updateStylePreview();
        });
        // Close picker on outside click
        document.addEventListener('click', function closeIconPicker(e) {
            if (!iconPicker.parentNode.contains(e.target)) {
                iconPicker.style.display = 'none';
            }
        });

        // File picker button — select SVG/ICO from filesystem
        document.getElementById('btnIconFile').addEventListener('click', async function(e) {
            e.stopPropagation();
            try {
                var dataUrl = await WailsBridge.pickIconFile();
                if (!dataUrl) return;
                selectedIcon = dataUrl;
                iconInput.value = '${t("sidebar.custom_icon")}';
                iconInput.setAttribute('readonly', 'readonly');
                updateStylePreview();
            } catch (err) {
                var msg = (err && (err.message || err + '')) || '未知错误';
                if (msg.indexOf('已取消') !== -1 || msg.indexOf('cancelled') !== -1) return;
                console.error('[PickIconFile]', err);
                App.showToast(t('toast.icon_select_failed') + ': ' + msg, 'error');
            }
        });

        const styleBgInput = document.getElementById('styleBgColor');
        const styleTextInput = document.getElementById('styleTextColor');
        styleBgInput.addEventListener('input', () => { styleBgColor = styleBgInput.value; updateStylePreview(); });
        styleTextInput.addEventListener('input', () => { styleTextColor = styleTextInput.value; updateStylePreview(); });

        document.getElementById('btnCreateStyleTag').addEventListener('click', async () => {
            const name = nameInput.value.trim();
            // 允许名称为空${t("sidebar.icon_optional")}
            const hasIcon = !!selectedIcon;
            const showName = document.getElementById('newTagShowName')?.checked !== false;
            try {
                const parentId = document.getElementById('newTagParent')?.value || null;
                await Storage.addTag({
                    id: 'tag_' + Date.now(), name, color: styleBgColor,
                    fillStyle: selectedFillStyle, fillOpacity: selectedFillOpacity, shape: selectedShape, icon: selectedIcon,
                    borderStyle: selectedBorderStyle, borderOpacity: selectedBorderOpacity, size: selectedSize, textSize: selectedTextSize, weight: selectedWeight,
                    iconOnly: !name && hasIcon,
                    iconOnlySize: hasIcon ? iconOnlySize : undefined,
                    showName: showName,
                    bgColor: styleBgColor, textColor: styleTextColor,
                    parentId: parentId || null, createdAt: new Date().toISOString()
                });
                closeDialog();
                await refreshTagTree();
                App.showToast(t('sidebar.tag_created'), 'success');
            } catch (err) {
                App.showToast(t('sidebar.tag_create_failed') + ': ' + err.message, 'error');
            }
        });

        // ============ 头像标签侧逻辑 ============
        const avatarNameInput = document.getElementById('avatarTagName');
        const avatarUploadArea = document.getElementById('avatarUploadArea');
        const avatarBgColorInput = document.getElementById('avatarBgColor');
        const avatarTextColorInput = document.getElementById('avatarTextColor');
        const avatarPreviewCont = document.getElementById('avatarPreview');
        const avatarFileInput = document.getElementById('avatarFileInput');

        function getShapeRadius(shape) {
            if (shape === 'sharp') return { outer: 2, thumb: 2 };
            if (shape === 'soft') return { outer: 6, thumb: 4 };
            if (shape === 'round') return { outer: 14, thumb: 10 };
            if (shape === 'pill') return { outer: 99, thumb: 12 };
            return { outer: 14, thumb: 10 }; // default round
        }

        function updateAvatarPreview() {
            const name = avatarNameInput.value.trim() || t('sidebar.preview');
            const showName = document.getElementById('avatarTagShowName')?.checked !== false;
            const bgColor = avatarBgColorInput.value;
            const textColor = avatarTextColorInput.value;
            const r = getShapeRadius(avatarShape);
            const ts = avatarThumbSize;
            const labelFontSize = avatarTextSize;
            const thumbRad = ts * 0.25;
            const imgUrl = typeof WailsBridge !== 'undefined' && WailsBridge.getAvatarUrl
                ? WailsBridge.getAvatarUrl(avatarDataUrl) : avatarDataUrl;
            const imgHtml = imgUrl
                ? `<img class="av-thumb" src="${imgUrl}" style="width:${ts}px;height:${ts}px;border-radius:${thumbRad}px 0 0 ${thumbRad}px;object-fit:cover;" />`
                : `<div class="av-thumb" style="width:${ts}px;height:${ts}px;border-radius:${thumbRad}px 0 0 ${thumbRad}px;background:var(--bg-hover);display:flex;align-items:center;justify-content:center;font-size:${ts*0.4}px;"><span class="icon icon-image"></span></div>`;
            const labelHtml = (avatarImageOnly || !showName) ? '' : `<span class="av-label" style="background:${bgColor};color:${textColor};font-size:${labelFontSize}px;border-radius:0 ${r.outer}px ${r.outer}px 0;">${escapeHtmlSidebar(name)}</span>`;
            avatarPreviewCont.innerHTML = imgHtml + labelHtml;
            avatarPreviewCont.style.borderRadius = r.outer + 'px';
        }

        avatarNameInput.addEventListener('input', updateAvatarPreview);
        avatarBgColorInput.addEventListener('input', updateAvatarPreview);
        avatarTextColorInput.addEventListener('input', updateAvatarPreview);
        const avatarShowNameCb = document.getElementById('avatarTagShowName');
        if (avatarShowNameCb) avatarShowNameCb.addEventListener('change', updateAvatarPreview);

        bindOptionRow('tagAvatarShape', v => { avatarShape = v; updateAvatarPreview(); });

        // 头像大小 slider
        const newAvatarThumbSizeSlider = document.getElementById('newAvatarThumbSizeSlider');
        const newAvatarThumbSizeVal = document.getElementById('newAvatarThumbSizeVal');
        if (newAvatarThumbSizeSlider) {
            newAvatarThumbSizeSlider.addEventListener('input', function() {
                avatarThumbSize = parseInt(this.value);
                if (newAvatarThumbSizeVal) newAvatarThumbSizeVal.textContent = this.value + 'px';
                updateAvatarPreview();
            });
        }

        // 头像文字大小 slider
        const newAvatarFontSizeSlider = document.getElementById('newAvatarFontSizeSlider');
        const newAvatarFontSizeVal = document.getElementById('newAvatarFontSizeVal');
        if (newAvatarFontSizeSlider) {
            newAvatarFontSizeSlider.addEventListener('input', function() {
                avatarTextSize = parseInt(this.value);
                if (newAvatarFontSizeVal) newAvatarFontSizeVal.textContent = this.value + 'px';
                updateAvatarPreview();
            });
        }

        avatarUploadArea.addEventListener('click', () => avatarFileInput.click());
        avatarFileInput.addEventListener('change', () => {
            if (!avatarFileInput.files || !avatarFileInput.files[0]) return;
            avatarCropFile = avatarFileInput.files[0];
            openAvatarCrop();
        });

        // 裁切弹窗
        function openAvatarCrop() {
            const url = URL.createObjectURL(avatarCropFile);
            // Remove old crop overlay if any
            const oldCrop = document.querySelector('.crop-overlay');
            if (oldCrop) oldCrop.remove();

            const cropOverlay = document.createElement('div');
            cropOverlay.className = 'crop-overlay';
            cropOverlay.innerHTML = `
                <div class="crop-box">
                    <h4>调整裁切范围</h4>
                    <div class="crop-stage" id="cropStage">
                        <img id="cropImg" src="${url}" alt="" />
                        <div class="crop-frame"></div>
                    </div>
                    <div class="crop-controls">
                        <label>缩放</label>
                        <input type="range" id="cropZoom" min="100" max="400" value="100" step="1" />
                    </div>
                    <div class="crop-actions">
                        <button class="btn-secondary" id="btnCropCancel">${t("sidebar.cancel")}</button>
                        <button class="btn-primary" id="btnCropApply">${t("sidebar.confirm_crop")}</button>
                    </div>
                </div>
            `;
            document.body.appendChild(cropOverlay);

            const cropImg = document.getElementById('cropImg');
            const cropStage = document.getElementById('cropStage');
            const zoomRange = document.getElementById('cropZoom');

            cropImg.onload = () => {
                avatarCropImgNatW = cropImg.naturalWidth;
                avatarCropImgNatH = cropImg.naturalHeight;
                const stageW = 300, stageH = 300;
                const scaleToFit = Math.max(stageW / avatarCropImgNatW, stageH / avatarCropImgNatH);
                avatarCropState.scale = scaleToFit;
                avatarCropState.x = (stageW - avatarCropImgNatW * scaleToFit) / 2;
                avatarCropState.y = (stageH - avatarCropImgNatH * scaleToFit) / 2;
                zoomRange.min = Math.round(scaleToFit * 100);
                zoomRange.value = Math.round(scaleToFit * 100);
                applyCropTransform();
            };

            function applyCropTransform() {
                cropImg.style.transform = `translate(${avatarCropState.x}px,${avatarCropState.y}px) scale(${avatarCropState.scale})`;
            }

            function clampCrop() {
                const w = avatarCropImgNatW * avatarCropState.scale;
                const h = avatarCropImgNatH * avatarCropState.scale;
                if (avatarCropState.x > 0) avatarCropState.x = 0;
                if (avatarCropState.y > 0) avatarCropState.y = 0;
                if (avatarCropState.x + w < 300) avatarCropState.x = 300 - w;
                if (avatarCropState.y + h < 300) avatarCropState.y = 300 - h;
            }

            cropStage.addEventListener('mousedown', e => {
                avatarCropIsDrag = true;
                avatarCropDragSX = e.clientX; avatarCropDragSY = e.clientY;
                avatarCropDragOX = avatarCropState.x; avatarCropDragOY = avatarCropState.y;
                e.preventDefault();
            });
            window.addEventListener('mousemove', onCropMove);
            window.addEventListener('mouseup', onCropUp);
            cropStage.addEventListener('touchstart', e => {
                const t = e.touches[0];
                avatarCropIsDrag = true;
                avatarCropDragSX = t.clientX; avatarCropDragSY = t.clientY;
                avatarCropDragOX = avatarCropState.x; avatarCropDragOY = avatarCropState.y;
                e.preventDefault();
            }, { passive: false });
            window.addEventListener('touchmove', onCropTouchMove, { passive: false });
            window.addEventListener('touchend', onCropUp);

            function onCropMove(e) {
                if (!avatarCropIsDrag) return;
                avatarCropState.x = avatarCropDragOX + (e.clientX - avatarCropDragSX);
                avatarCropState.y = avatarCropDragOY + (e.clientY - avatarCropDragSY);
                clampCrop();
                applyCropTransform();
            }
            function onCropTouchMove(e) {
                if (!avatarCropIsDrag) return;
                const t = e.touches[0];
                avatarCropState.x = avatarCropDragOX + (t.clientX - avatarCropDragSX);
                avatarCropState.y = avatarCropDragOY + (t.clientY - avatarCropDragSY);
                clampCrop();
                applyCropTransform();
            }
            function onCropUp() {
                avatarCropIsDrag = false;
            }

            zoomRange.addEventListener('input', () => {
                const z = parseInt(zoomRange.value) / 100;
                const prev = avatarCropState.scale;
                const cx = 150, cy = 150;
                avatarCropState.x = cx - (cx - avatarCropState.x) * (z / prev);
                avatarCropState.y = cy - (cy - avatarCropState.y) * (z / prev);
                avatarCropState.scale = z;
                clampCrop();
                applyCropTransform();
            });

            document.getElementById('btnCropCancel').addEventListener('click', () => {
                cleanupCrop();
            });
            document.getElementById('btnCropApply').addEventListener('click', () => {
                // Render 256x256 cropped result
                const sx = -avatarCropState.x / avatarCropState.scale;
                const sy = -avatarCropState.y / avatarCropState.scale;
                const sw = 300 / avatarCropState.scale;
                const sh = 300 / avatarCropState.scale;
                const canvas = document.createElement('canvas');
                canvas.width = 256; canvas.height = 256;
                const ctx = canvas.getContext('2d');
                ctx.drawImage(cropImg, sx, sy, sw, sh, 0, 0, 256, 256);
                // 转为 Blob 保存，用 blob URL 预览
                canvas.toBlob((blob) => {
                    avatarCropBlob = blob;
                    avatarDataUrl = URL.createObjectURL(blob);
                    const area = document.getElementById('avatarUploadArea');
                    area.innerHTML = `<img src="${avatarDataUrl}" alt="avatar" /><div class="avatar-upload-cam"><span class="icon icon-image"></span></div>`;
                    area.classList.add('has-image');
                    updateAvatarPreview();
                }, 'image/jpeg', 0.85);
                cleanupCrop();
            });

            function cleanupCrop() {
                window.removeEventListener('mousemove', onCropMove);
                window.removeEventListener('mouseup', onCropUp);
                window.removeEventListener('touchmove', onCropTouchMove);
                window.removeEventListener('touchend', onCropUp);
                cropOverlay.remove();
                URL.revokeObjectURL(url);
            }

            cropOverlay.addEventListener('click', (e) => {
                if (e.target === cropOverlay) {
                    cleanupCrop();
                }
            });
        }

        document.getElementById('btnCreateAvatarTag').addEventListener('click', async () => {
            const name = avatarNameInput.value.trim();
            if (!avatarCropBlob && !avatarDataUrl) { App.showToast(t('toast.avatar_image_needed'), 'warning'); return; }
            const showName = document.getElementById('avatarTagShowName')?.checked !== false;
            try {
                // 将 Blob 转为 base64 保存到后端 avatar 文件
                let avatarPath = '';
                if (avatarCropBlob) {
                    const base64 = await new Promise((resolve, reject) => {
                        const reader = new FileReader();
                        reader.onload = () => resolve(reader.result);
                        reader.onerror = reject;
                        reader.readAsDataURL(avatarCropBlob);
                    });
                    if (typeof WailsBridge !== 'undefined' && WailsBridge.saveAvatar) {
                        avatarPath = await WailsBridge.saveAvatar(base64);
                    } else {
                        avatarPath = base64; // 非 Wails 环境回退到 base64
                    }
                } else {
                    avatarPath = avatarDataUrl; // 已有 base64（编辑场景）
                }
                const parentId = document.getElementById('newTagParent')?.value || null;
                await Storage.addTag({
                    id: 'tag_' + Date.now(), name: name,
                    tagType: 'avatar',
                    avatarData: avatarPath,
                    color: avatarTextColorInput.value,
                    bgColor: avatarBgColorInput.value,
                    shape: avatarShape,
                    avatarThumbSize: avatarThumbSize,
                    avatarTextSize: avatarTextSize,
                    fillStyle: 'solid',
                    borderStyle: 'solid',
                    size: 'md',
                    weight: 'regular',
                    icon: '',
                    showName: showName,
                    parentId: parentId || null,
                    createdAt: new Date().toISOString()
                });
                closeDialog();
                await refreshTagTree();
                App.showToast(t('sidebar.tag_created') + ': ' + name, 'success');
            } catch (err) {
                App.showToast(t('sidebar.tag_create_failed') + ': ' + err.message, 'error');
            }
        });

        document.getElementById('btnCreateHtmlTag').addEventListener('click', async () => {
            const name = document.getElementById('htmlTagName').value.trim();
            const code = document.getElementById('htmlTagCode').value.trim();
            const width = parseInt(document.getElementById('htmlTagWidth').value) || 120;
            const height = parseInt(document.getElementById('htmlTagHeight').value) || 40;
            const showName = document.getElementById('htmlTagShowName')?.checked !== false;
            if (!name) { App.showToast(t('sidebar.enter_tag_name'), 'warning'); return; }
            if (!code) { App.showToast(t('sidebar.enter_html_code'), 'warning'); return; }
            try {
                const parentId = document.getElementById('htmlTagParent')?.value || null;
                await Storage.addTag({
                    id: 'tag_' + Date.now(), name: name,
                    tagType: 'html',
                    htmlCode: code,
                    htmlWidth: width,
                    htmlHeight: height,
                    showName: showName,
                    color: '#ffffff',
                    bgColor: 'transparent',
                    shape: 'soft',
                    fillStyle: 'solid',
                    borderStyle: 'solid',
                    size: 'md',
                    weight: 'regular',
                    icon: '',
                    parentId: parentId || null,
                    createdAt: new Date().toISOString()
                });
                closeDialog();
                await refreshTagTree();
                App.showToast(t('sidebar.html_tag_created'), 'success');
            } catch (err) {
                App.showToast(t('sidebar.tag_create_failed') + ': ' + err.message, 'error');
            }
        });

        // HTML 代码实时预览
        const htmlTagCodeEl = document.getElementById('htmlTagCode');
        const htmlTagPreviewEl = document.getElementById('htmlTagPreviewBox');
        if (htmlTagCodeEl && htmlTagPreviewEl) {
            let htmlPreviewTimer = null;
            const htmlPreviewScopeId = 'new-html-preview';
            const htmlTagWidthInput = document.getElementById('htmlTagWidth');
            const htmlTagHeightInput = document.getElementById('htmlTagHeight');
            htmlTagCodeEl.addEventListener('input', () => {
                clearTimeout(htmlPreviewTimer);
                htmlPreviewTimer = setTimeout(() => {
                    const code = htmlTagCodeEl.value;
                    if (!code.trim()) {
                        htmlTagPreviewEl.removeAttribute('data-tag-scope');
                        htmlTagPreviewEl.innerHTML = '<span style="color:var(--text-muted);font-size:12px;">' + t('sidebar.preview_area') + '</span>';
                        htmlTagPreviewEl.style.width = '';
                        htmlTagPreviewEl.style.height = '';
                        return;
                    }
                    const w = htmlTagWidthInput ? parseInt(htmlTagWidthInput.value) || 120 : 120;
                    const h = htmlTagHeightInput ? parseInt(htmlTagHeightInput.value) || 40 : 40;
                    htmlTagPreviewEl.style.width = w + 'px';
                    htmlTagPreviewEl.style.height = h + 'px';
                    applyScopeToContainer(htmlTagPreviewEl, code, htmlPreviewScopeId);
                }, 300);
            });

            // 宽高输入框变化时实时更新预览尺寸并重新缩放
            const rescalePreview = () => {
                const inner = htmlTagPreviewEl.querySelector('.html-preview-inner');
                if (!inner) return;
                // 重置 transform 才能测量真实尺寸
                inner.style.transform = '';
                requestAnimationFrame(() => {
                    const rect = inner.getBoundingClientRect();
                    const cw = htmlTagPreviewEl.clientWidth;
                    const ch = htmlTagPreviewEl.clientHeight;
                    if (!cw || !ch) return;
                    const nw = rect.width || cw;
                    const nh = rect.height || ch;
                    const scale = Math.min(cw / nw, ch / nh, 1);
                    inner.style.transform = `scale(${scale})`;
                    const scaledW = nw * scale;
                    const scaledH = nh * scale;
                    inner.style.left = ((cw - scaledW) / 2) + 'px';
                    inner.style.top = ((ch - scaledH) / 2) + 'px';
                });
            };
            if (htmlTagWidthInput) {
                htmlTagWidthInput.addEventListener('input', () => {
                    const w = parseInt(htmlTagWidthInput.value) || 120;
                    htmlTagPreviewEl.style.width = w + 'px';
                    rescalePreview();
                });
            }
            if (htmlTagHeightInput) {
                htmlTagHeightInput.addEventListener('input', () => {
                    const h = parseInt(htmlTagHeightInput.value) || 40;
                    htmlTagPreviewEl.style.height = h + 'px';
                    rescalePreview();
                });
            }
        }

        // 填充父标签下拉框（HTML标签列也需要）
        const htmlParentSelect = document.getElementById('htmlTagParent');
        if (htmlParentSelect) {
            htmlParentSelect.innerHTML = '<option value="">' + t('sidebar.no_parent_root') + '</option>';
            tags.forEach(t => {
                htmlParentSelect.innerHTML += `<option value="${t.id}">${escapeHtmlSidebar(t.name || '(空)')}</option>`;
            });
        }

        // ============ 取消按钮 ============
        content.querySelectorAll('.btn-cancel-dialog').forEach(btn => {
            btn.addEventListener('click', () => closeDialog());
        });

        // ============ 通用关闭 ============
        overlay.addEventListener('click', (e) => {
            if (e.target === overlay) closeDialog();
        });

        setTimeout(() => nameInput.focus(), 100);
    }

    /**
     * 显示编辑标签对话框 — 支持修改标签所有属性 + 文本/头像类型切换
     */
    function showEditTagDialog(tag) {
        const overlay = document.getElementById('modalOverlay');
        const content = document.getElementById('modalContent');

        const isAvatar = tag.tagType === 'avatar';
        const isHtml = tag.tagType === 'html';

        // Pre-fill from existing tag
        let iconOnly = tag.iconOnly === true;
        let iconOnlySize = typeof tag.iconOnlySize === 'number' ? tag.iconOnlySize : 16;
        let selectedFillStyle = (tag.fillStyle === 'dark' || tag.fillStyle === 'light') ? 'solid' : (tag.fillStyle || 'solid');
        let selectedFillOpacity = typeof tag.fillOpacity === 'number' ? tag.fillOpacity : 100;
        let selectedShape = tag.shape || 'soft';
        let selectedIcon = tag.icon || '';
        let selectedBorderStyle = tag.borderStyle || 'solid';
        let selectedBorderOpacity = typeof tag.borderOpacity === 'number' ? tag.borderOpacity : 100;
        let selectedSize = tag.size || 'md';
        let selectedTextSize = typeof tag.textSize === 'number' ? tag.textSize : 14;
        let selectedWeight = tag.weight || 'regular';
        let styleBgColor = tag.bgColor || tag.color || '#9b59b6';
        let styleTextColor = tag.textColor || '#ffffff';

        // Avatar state
        let avatarDataUrl = tag.avatarData || null;
        // 将文件路径转为完整 HTTP URL，供 <img src> 使用
        if (avatarDataUrl && typeof WailsBridge !== 'undefined' && WailsBridge.getAvatarUrl) {
            avatarDataUrl = WailsBridge.getAvatarUrl(avatarDataUrl);
        }
        let avatarCropBlob = null;   // 裁剪后的 Blob，用于保存到文件
        let avatarBgColor = tag.bgColor || '#2a2a2a';
        let avatarTextColor = tag.color || '#ffffff';
        let avatarTextSize = typeof tag.avatarTextSize === 'number' ? tag.avatarTextSize : 12;
        let avatarThumbSize = typeof tag.avatarThumbSize === 'number' ? tag.avatarThumbSize : 40;
        let avatarShape = tag.shape || 'round';
        let avatarImageOnly = tag.avatarImageOnly === true;
        let avatarCropFile = null;
        let avatarCropImgNatW = 0, avatarCropImgNatH = 0;
        let avatarCropState = { x: 0, y: 0, scale: 1 };
        let avatarCropIsDrag = false;
        let avatarCropDragSX = 0, avatarCropDragSY = 0, avatarCropDragOX = 0, avatarCropDragOY = 0;

        // HTML tag state
        let htmlCode = tag.htmlCode || '';
        let htmlWidth = tag.htmlWidth || 120;
        let htmlHeight = tag.htmlHeight || 40;

        // Current type (can be toggled)
        let currentType = isHtml ? 'html' : (isAvatar ? 'avatar' : 'text');

        function closeDialog() { content.classList.remove('wide'); overlay.style.display = 'none'; }


        const typeToggleHtml = `
            <div class="tag-style-section" style="margin-bottom:14px;padding:10px;background:var(--bg-primary);border-radius:8px;border:1px solid var(--border-color);">
                <label style="margin-bottom:6px;">${t("sidebar.tag_type")}</label>
                <div class="tag-option-row" id="tagTypeToggle">
                    <button class="tag-option-btn${currentType === 'text' ? ' selected' : ''}" data-value="text"><span class="icon icon-edit"></span> ${t("sidebar.text_tag")}</button>
                    <button class="tag-option-btn${currentType === 'avatar' ? ' selected' : ''}" data-value="avatar"><span class="icon icon-user"></span> ${t("sidebar.avatar_tag")}</button>
                    <button class="tag-option-btn${currentType === 'html' ? ' selected' : ''}" data-value="html"><span class="icon icon-art"></span> ${t("sidebar.html_tag")}</button>
                </div>
                <div style="font-size:11px;color:var(--text-muted);margin-top:6px;" id="typeToggleHint">
                    ${currentType === 'html' ? t('sidebar.html_tag_customizable') : (currentType === 'text' ? t('sidebar.switch_to_avatar_needs_image') : t('sidebar.switch_to_text_discards_avatar'))}
                </div>
            </div>`;

        content.innerHTML = `
            <h2><span class="icon icon-edit"></span> ${t("sidebar.edit_tag")}「${escapeHtmlSidebar(tag.name)}」</h2>
            <div class="modal-actions" style="margin-top:0;margin-bottom:16px;">
                <button class="btn-secondary btn-cancel-dialog">${t("sidebar.cancel")}</button>
                <button id="btnSaveEditTag" class="btn-primary"><span class="icon icon-save"></span> ${t("sidebar.save_changes")}</button>
            </div>
            ${typeToggleHtml}
            <div class="form-group" style="margin-bottom:14px;">
                <label>${t("sidebar.tag_name")} <span style="color:var(--text-muted);font-weight:400;">${t("sidebar.icon_optional")}</span></label>
                <div class="tag-name-row">
                    <input type="text" id="editTagName" value="${escapeHtmlSidebar(tag.name)}" placeholder="输入${t("sidebar.tag_name")}..." style="flex:1;" />
                    <label class="tag-name-show-cb"><input type="checkbox" id="editTagShowName" ${tag.showName !== false ? 'checked' : ''} /></label>
                </div>
            </div>
            <div class="form-group" style="margin-bottom:14px;">
                <label>${t("sidebar.parent_tag_optional")}</label>
                <select id="editTagParent" style="width:100%;padding:6px 8px;background:var(--bg-input);border:1px solid var(--border-color);border-radius:var(--radius-sm);color:var(--text-primary);">
                    <option value="">${t("sidebar.no_parent_root")}</option>
                </select>
            </div>
            <div class="new-tag-columns">
                <div class="new-tag-column" id="textPanel" style="${currentType === 'avatar' ? 'opacity:0.4;pointer-events:none;' : ''}">
                    <h3><span class="icon icon-art"></span> ${t("sidebar.style_tag")}</h3>
                    <div class="tag-style-section">
                        <label>${t("sidebar.bg_color")}</label>
                        <div class="avatar-shadow-row">
                            <input type="color" id="editStyleBgColor" value="${styleBgColor}" />
                        </div>
                    </div>
                    <div class="tag-style-section">
                        <label>${t("sidebar.text_color")}</label>
                        <div class="avatar-shadow-row">
                            <input type="color" id="editStyleTextColor" value="${styleTextColor}" />
                        </div>
                    </div>
                    <div class="tag-style-section">
                        <label>${t("sidebar.fill_style")}</label>
                        <div class="tag-option-row" id="editTagFillStyle">
                            <button class="tag-option-btn${selectedFillStyle==='solid'?' selected':''}" data-value="solid">${t("sidebar.fill_solid")}</button>
                            <button class="tag-option-btn${selectedFillStyle==='outline'?' selected':''}" data-value="outline">${t("sidebar.fill_outline")}</button>
                        </div>
                        <div class="tag-opacity-row">
                            <label class="tag-opacity-label">${t("sidebar.opacity")}</label>
                            <input type="range" id="editTagFillOpacity" min="0" max="100" value="${selectedFillOpacity}" step="5" />
                            <span class="tag-opacity-value">${selectedFillOpacity}%</span>
                        </div>
                    </div>
                    <div class="tag-style-section">
                        <label>${t("sidebar.shape")}</label>
                        <div class="tag-option-row" id="editTagShape">
                            <button class="tag-option-btn${selectedShape==='sharp'?' selected':''}" data-value="sharp">${t("sidebar.shape_sharp")}</button>
                            <button class="tag-option-btn${selectedShape==='soft'?' selected':''}" data-value="soft">${t("sidebar.shape_soft")}</button>
                            <button class="tag-option-btn${selectedShape==='pill'?' selected':''}" data-value="pill">${t("sidebar.shape_pill")}</button>
                            <button class="tag-option-btn${selectedShape==='leftbar'?' selected':''}" data-value="leftbar">${t("sidebar.shape_leftbar")}</button>
                        </div>
                    </div>
                    <div class="tag-style-section">
                        <label>${t("sidebar.prefix_icon")}</label>
                        <div class="tag-icon-input-row">
                            <input type="text" id="editTagIconInput" value="${escapeHtmlSidebar(selectedIcon.indexOf('data:')===0?'${t("sidebar.custom_icon")}':selectedIcon)}" placeholder="${t("sidebar.icon_input_hint")}" maxlength="4" ${selectedIcon.indexOf('data:')===0?'readonly':''} />
                            <button class="btn-small" id="btnEditIconPicker" type="button">${t("sidebar.select_icon")}</button>
                            <button class="btn-small" id="btnEditIconFile" type="button" title="${t("sidebar.icon_from_file")}">📁</button>
                        </div>
                        <div class="tag-icon-picker" id="editTagIconPicker" style="display:none;">${buildIconPickerHTML()}</div>
                    </div>
                    <div class="tag-style-section">
                        <label>${t("sidebar.border")}</label>
                        <div class="tag-option-row" id="editTagBorderStyle">
                            <button class="tag-option-btn${selectedBorderStyle==='solid'?' selected':''}" data-value="solid">${t("sidebar.border_solid")}</button>
                            <button class="tag-option-btn${selectedBorderStyle==='dashed'?' selected':''}" data-value="dashed">${t("sidebar.border_dashed")}</button>
                            <button class="tag-option-btn${selectedBorderStyle==='none'?' selected':''}" data-value="none">${t("sidebar.border_none")}</button>
                        </div>
                        <div class="tag-opacity-row" style="margin-top:8px;">
                            <label class="tag-opacity-label">${t("sidebar.border")}${t("sidebar.opacity")}</label>
                            <input type="range" id="editBorderOpacitySlider" min="0" max="100" value="${selectedBorderOpacity}" step="5" />
                            <span class="tag-opacity-value" id="editBorderOpacityVal">${selectedBorderOpacity}%</span>
                        </div>
                    </div>
                    <div class="tag-style-section">
                        <label>尺寸</label>
                        <div class="tag-option-row" id="editTagSize">
                            <button class="tag-option-btn${selectedSize==='sm'?' selected':''}" data-value="sm">${t("sidebar.size_small")}</button>
                            <button class="tag-option-btn${selectedSize==='md'?' selected':''}" data-value="md">${t("sidebar.size_default")}</button>
                            <button class="tag-option-btn${selectedSize==='lg'?' selected':''}" data-value="lg">${t("sidebar.size_large")}</button>
                        </div>
                        <div class="tag-opacity-row" style="margin-top:8px;">
                            <label class="tag-opacity-label">${t("sidebar.font_size")}</label>
                            <input type="range" id="editTextSizeSlider" min="10" max="60" value="${selectedTextSize}" step="1" />
                            <span class="tag-opacity-value" id="editTextSizeVal">${selectedTextSize}px</span>
                        </div>
                        <div class="tag-opacity-row" style="margin-top:8px;">
                            <label class="tag-opacity-label">${t("sidebar.icon_size")}</label>
                            <input type="range" id="editIconOnlySizeSlider" min="12" max="300" value="${iconOnlySize}" step="1">
                            <span class="tag-opacity-value" id="editIconOnlySizeVal">${iconOnlySize}px</span>
                        </div>
                    </div>
                    <div class="tag-style-section">
                        <label>${t("sidebar.font_weight")}</label>
                        <div class="tag-option-row" id="editTagWeight">
                            <button class="tag-option-btn${selectedWeight==='regular'?' selected':''}" data-value="regular">${t("sidebar.weight_regular")}</button>
                            <button class="tag-option-btn${selectedWeight==='bold'?' selected':''}" data-value="bold">${t("sidebar.weight_bold")}</button>
                        </div>
                    </div>
                    <div class="tag-preview-container">
                        <span id="editTagPreview" class="tag-badge tag-fill-solid tag-shape-pill tag-size-md tag-weight-regular" style="background:#9b59b6;color:#fff;border:1.5px solid #9b59b6;"></span>
                    </div>
                </div>
                <div class="new-tag-divider"></div>
                <div class="new-tag-column" id="avatarPanel" style="${currentType === 'text' ? 'opacity:0.4;pointer-events:none;' : ''}">
                    <h3><span class="icon icon-user"></span> ${t("sidebar.avatar_tag")}</h3>
                    <div class="avatar-row">
                        <div class="avatar-upload-area${avatarDataUrl ? ' has-image' : ''}" id="editAvatarUploadArea" title="${t("sidebar.click_upload_avatar")}">
                            ${avatarDataUrl ? `<img src="${avatarDataUrl}" alt="avatar" /><div class="avatar-upload-cam"><span class="icon icon-image"></span></div>` : '<span class="avatar-upload-placeholder"><span class="icon icon-image"></span></span><div class="avatar-upload-cam"><span class="icon icon-image"></span></div>'}
                        </div>
                        <div class="avatar-info">
                            <div class="avatar-shadow-row">
                                <label style="font-size:11px;color:var(--text-secondary);">${t("sidebar.bg_color")}</label>
                                <input type="color" id="editAvatarBgColor" value="${avatarBgColor}" />
                            </div>
                            <div class="avatar-shadow-row">
                                <label style="font-size:11px;color:var(--text-secondary);">${t("sidebar.text_color")}</label>
                                <input type="color" id="editAvatarTextColor" value="${avatarTextColor}" />
                            </div>
                            <div class="tag-style-section" style="margin-top:8px;">
                                <label>${t("sidebar.shape")}</label>
                                <div class="tag-option-row" id="editTagAvatarShape">
                                    <button class="tag-option-btn${avatarShape==='sharp'?' selected':''}" data-value="sharp">${t("sidebar.shape_sharp")}</button>
                                    <button class="tag-option-btn${avatarShape==='soft'?' selected':''}" data-value="soft">${t("sidebar.shape_soft")}</button>
                                    <button class="tag-option-btn${avatarShape==='round'?' selected':''}" data-value="round">${t("sidebar.shape_round")}</button>
                                </div>
                                <div class="tag-opacity-row" style="margin-top:8px;">
                                    <label class="tag-opacity-label">${t("sidebar.avatar_thumb_size")}</label>
                                    <input type="range" id="editAvatarThumbSizeSlider" min="24" max="300" value="${avatarThumbSize}" step="2" />
                                    <span class="tag-opacity-value" id="editAvatarThumbSizeVal">${avatarThumbSize}px</span>
                                </div>
                                <div class="tag-opacity-row" style="margin-top:8px;">
                                    <label class="tag-opacity-label">${t("sidebar.avatar_font_size")}</label>
                                    <input type="range" id="editAvatarFontSizeSlider" min="8" max="60" value="${avatarTextSize}" step="1" />
                                    <span class="tag-opacity-value" id="editAvatarFontSizeVal">${avatarTextSize}px</span>
                                </div>
                            </div>
                        </div>
                    </div>
                    <div class="tag-preview-container" style="margin-top:10px;">
                        <div class="avatar-preview-tag" id="editAvatarPreview" style="border-radius:14px;">
                            ${avatarDataUrl
                                ? `<img class="av-thumb" src="${avatarDataUrl}" style="width:40px;height:40px;border-radius:10px 0 0 10px;object-fit:cover;" />`
                                : `<div class="av-thumb" style="width:40px;height:40px;border-radius:10px 0 0 10px;background:var(--bg-hover);display:flex;align-items:center;justify-content:center;font-size:16px;"><span class="icon icon-image"></span></div>`}
                            <span class="av-label" style="background:${avatarBgColor};color:${avatarTextColor};border-radius:0 14px 14px 0;">${escapeHtmlSidebar(tag.name)}</span>
                        </div>
                    </div>
                </div>
                <div class="new-tag-divider"></div>
                <div class="new-tag-column" id="htmlPanel" style="${currentType !== 'html' ? 'opacity:0.4;pointer-events:none;' : ''}">
                    <h3><span class="icon icon-art"></span> ${t("sidebar.html_tag")}</h3>
                    <div class="form-group">
                        <label>${t("sidebar.tag_width")}</label>
                        <div style="display:flex;align-items:center;gap:8px;">
                            <input type="number" id="editHtmlWidth" value="${htmlWidth}" min="10" max="800" step="1" style="width:100px;" />
                            <span style="font-size:12px;color:var(--text-muted);">px</span>
                            <span style="font-size:12px;color:var(--text-muted);">${t("sidebar.tag_height")}</span>
                            <input type="number" id="editHtmlHeight" value="${htmlHeight}" min="10" max="800" step="1" style="width:100px;" />
                            <span style="font-size:12px;color:var(--text-muted);">px</span>
                        </div>
                    </div>
                    <div class="form-group">
                        <label>${t("sidebar.html_css_code")}</label>
                        <textarea id="editHtmlCode" class="html-tag-code-editor" rows="12">${escapeHtmlSidebar(htmlCode)}</textarea>
                    </div>
                    <div class="form-group">
                        <label>${t("sidebar.preview")}</label>
                        <div class="html-tag-preview-box" id="editHtmlPreviewBox" style="min-height:50px;border:1px dashed var(--border-color);border-radius:4px;padding:6px;display:flex;align-items:center;justify-content:center;overflow:hidden;"></div>
                    </div>
                </div>
            </div>
            <div class="preset-colors-section">
                <label>${t("sidebar.preset_palette")}</label>
                <div class="preset-colors-grid" id="presetColorsGrid"></div>
            </div>
            <input type="file" id="editAvatarFileInput" accept="image/*" style="display:none;" />
        `;

        content.classList.add('wide');
        overlay.style.display = 'flex';

        // ============ 填充父标签下拉 ============
        const editParentSelect = document.getElementById('editTagParent');
        if (editParentSelect && tags.length > 0) {
            const buildParentOptions = (tagList, indent = 0, excludeId = '') => {
                let html = '';
                for (const t of tagList) {
                    if (t.id === excludeId) continue; // 不能选自己
                    html += `<option value="${t.id}"${t.id === (tag.parentId || '') ? ' selected' : ''}>${'　'.repeat(indent)}${indent > 0 ? '└ ' : ''}${escapeHtmlSidebar(t.name)}</option>`;
                    const children = tags.filter(c => c.parentId === t.id);
                    if (children.length) html += buildParentOptions(children, indent + 1, excludeId);
                }
                return html;
            };
            editParentSelect.innerHTML = `<option value="">${t("sidebar.no_parent_root")}</option>` + buildParentOptions(tags.filter(t => !t.parentId), 0, tag.id);
        }

        // ============ Type toggle ============
        content.querySelectorAll('#tagTypeToggle .tag-option-btn').forEach(btn => {
            btn.addEventListener('click', () => {
                content.querySelectorAll('#tagTypeToggle .tag-option-btn').forEach(b => b.classList.remove('selected'));
                btn.classList.add('selected');
                currentType = btn.dataset.value;
                const textPanel = document.getElementById('textPanel');
                const avatarPanel = document.getElementById('avatarPanel');
                const htmlPanel = document.getElementById('htmlPanel');
                const hint = document.getElementById('typeToggleHint');
                if (currentType === 'text') {
                    textPanel.style.opacity = '1'; textPanel.style.pointerEvents = 'auto';
                    avatarPanel.style.opacity = '0.4'; avatarPanel.style.pointerEvents = 'none';
                    if (htmlPanel) { htmlPanel.style.opacity = '0.4'; htmlPanel.style.pointerEvents = 'none'; }
                    hint.textContent = t('sidebar.switch_to_avatar_needs_image');
                } else if (currentType === 'html') {
                    if (htmlPanel) { htmlPanel.style.opacity = '1'; htmlPanel.style.pointerEvents = 'auto'; }
                    textPanel.style.opacity = '0.4'; textPanel.style.pointerEvents = 'none';
                    avatarPanel.style.opacity = '0.4'; avatarPanel.style.pointerEvents = 'none';
                    hint.textContent = t('sidebar.html_tag_customizable');
                } else {
                    avatarPanel.style.opacity = '1'; avatarPanel.style.pointerEvents = 'auto';
                    textPanel.style.opacity = '0.4'; textPanel.style.pointerEvents = 'none';
                    if (htmlPanel) { htmlPanel.style.opacity = '0.4'; htmlPanel.style.pointerEvents = 'none'; }
                    hint.textContent = t('sidebar.switch_to_text_discards_avatar');
                }
            });
        });

        // ============ Preset palette ============
        const DEFAULT_PRESETS = [
            '#ff0000','#ff4500','#ff8c00','#ffd700','#ffff00','#adff2f','#00ff00','#00ced1','#1e90ff','#0000ff',
            '#8a2be2','#ff00ff','#ff1493','#dc143c','#c0c0c0','#808080','#ffffff','#000000','#8b4513','#f5deb3'
        ];
        let colorPresets = [];
        try {
            const saved = localStorage.getItem('tagColorPresets');
            colorPresets = saved ? JSON.parse(saved) : [...DEFAULT_PRESETS];
        } catch (e) { colorPresets = [...DEFAULT_PRESETS]; }
        let lastFocusedColorInput = null;

        function savePresets() {
            saveTagColorPresetsToServer(colorPresets);
        }

        function renderPresetGrid() {
            const grid = document.getElementById('presetColorsGrid');
            if (!grid) return;
            grid.innerHTML = colorPresets.map((c, i) =>
                `<div class="preset-color-swatch" data-index="${i}" style="background:${c};" title="${c} — ${t("sidebar.left_click_pick_right_click_save")}"></div>`
            ).join('');
            grid.querySelectorAll('.preset-color-swatch').forEach(swatch => {
                swatch.addEventListener('click', (e) => {
                    if (!lastFocusedColorInput) return;
                    const color = colorPresets[parseInt(swatch.dataset.index)];
                    lastFocusedColorInput.value = color;
                    lastFocusedColorInput.dispatchEvent(new Event('input', { bubbles: true }));
                });
                swatch.addEventListener('contextmenu', (e) => {
                    e.preventDefault();
                    if (!lastFocusedColorInput) return;
                    const idx = parseInt(swatch.dataset.index);
                    colorPresets[idx] = lastFocusedColorInput.value;
                    savePresets();
                    swatch.style.background = lastFocusedColorInput.value;
                    swatch.classList.remove('saved');
                    void swatch.offsetWidth;
                    swatch.classList.add('saved');
                });
            });
        }
        renderPresetGrid();

        content.querySelectorAll('input[type="color"]').forEach(input => {
            input.addEventListener('focus', () => { lastFocusedColorInput = input; });
        });
        const firstColorInput = content.querySelector('input[type="color"]');
        if (firstColorInput) lastFocusedColorInput = firstColorInput;

        // ============ Text style panel logic ============
        const stylePreview = document.getElementById('editTagPreview');
        const nameInput = document.getElementById('editTagName');

        function updateStylePreview() {
            try {
                const name = nameInput && nameInput.value ? nameInput.value.trim() || t('sidebar.tag_preview') : t('sidebar.tag_preview');
                const showName = document.getElementById('editTagShowName')?.checked !== false;
                if (stylePreview) {
                    const isIconOnly = selectedIcon && !name;
                    if (isIconOnly || !showName) {
                        let iconHtml = '';
                        if (selectedIcon) {
                            const sz = iconOnlySize + 'px';
                            if (selectedIcon.indexOf('data:') === 0) {
                                iconHtml = '<img src="' + selectedIcon + '" style="width:' + sz + ';height:' + sz + ';object-fit:contain;" alt="" />';
                            } else {
                                iconHtml = '<span style="font-size:' + sz + ';line-height:1;">' + selectedIcon + '</span>';
                            }
                        }
                        stylePreview.innerHTML = iconHtml;
                        stylePreview.className = 'tag-badge';
                        stylePreview.removeAttribute('style');
                        TagStyle.apply(stylePreview, {
                            color: styleBgColor, fillStyle: selectedFillStyle, fillOpacity: selectedFillOpacity, shape: selectedShape,
                            icon: selectedIcon, borderStyle: selectedBorderStyle, borderOpacity: selectedBorderOpacity, size: selectedSize, textSize: selectedTextSize, weight: selectedWeight,
                            bgColor: styleBgColor, textColor: styleTextColor
                        });
                    } else {
                        let iconHtml = '';
                        if (selectedIcon) {
                            const sz = iconOnlySize + 'px';
                            if (selectedIcon.indexOf('data:') === 0) {
                                iconHtml = '<img src="' + selectedIcon + '" class="tag-icon-img" style="width:' + sz + ';height:' + sz + ';" alt="" />';
                            } else {
                                iconHtml = '<span style="font-size:' + sz + ';line-height:1;">' + selectedIcon + '</span>';
                            }
                        }
                        stylePreview.innerHTML = iconHtml + '<span>' + escapeHtmlSidebar(name) + '</span>';
                        stylePreview.className = 'tag-badge';
                        stylePreview.removeAttribute('style');
                        TagStyle.apply(stylePreview, {
                            color: styleBgColor, fillStyle: selectedFillStyle, fillOpacity: selectedFillOpacity, shape: selectedShape,
                            icon: selectedIcon, borderStyle: selectedBorderStyle, borderOpacity: selectedBorderOpacity, size: selectedSize, textSize: selectedTextSize, weight: selectedWeight,
                            bgColor: styleBgColor, textColor: styleTextColor
                        });
                    }
                }
            } catch (e) {
                console.warn('[Sidebar] updateStylePreview error:', e);
            }
        }
        updateStylePreview();
        if (nameInput) nameInput.addEventListener('input', updateStylePreview);
        const editShowNameCb = document.getElementById('editTagShowName');
        if (editShowNameCb) editShowNameCb.addEventListener('change', () => { updateStylePreview(); updateAvatarPreview(); });

        function bindOptionRow(rowId, setter) {
            const row = document.getElementById(rowId);
            if (!row) return;
            row.querySelectorAll('.tag-option-btn').forEach(btn => {
                btn.addEventListener('click', () => {
                    try {
                        row.querySelectorAll('.tag-option-btn').forEach(b => b.classList.remove('selected'));
                        btn.classList.add('selected');
                        setter(btn.dataset.value);
                        updateStylePreview();
                    } catch (e) {
                        console.warn('[Sidebar] bindOptionRow click error:', e);
                    }
                });
            });
        }
        bindOptionRow('editTagFillStyle', v => { selectedFillStyle = v; });
        bindOptionRow('editTagShape', v => { selectedShape = v; });
        bindOptionRow('editTagBorderStyle', v => { selectedBorderStyle = v; });
        bindOptionRow('editTagSize', v => { selectedSize = v; });
        bindOptionRow('editTagWeight', v => { selectedWeight = v; });

        // Icon size slider (always visible in edit dialog)
        var editIconOnlySizeSlider = document.getElementById('editIconOnlySizeSlider');
        var editIconOnlySizeVal = document.getElementById('editIconOnlySizeVal');
        if (editIconOnlySizeSlider) {
            editIconOnlySizeSlider.addEventListener('input', function() {
                iconOnlySize = parseInt(this.value);
                if (editIconOnlySizeVal) editIconOnlySizeVal.textContent = iconOnlySize + 'px';
                updateStylePreview();
            });
        }

        // Text size slider for edit dialog
        var editTextSizeSlider = document.getElementById('editTextSizeSlider');
        var editTextSizeVal = document.getElementById('editTextSizeVal');
        if (editTextSizeSlider) {
            editTextSizeSlider.addEventListener('input', function() {
                selectedTextSize = parseInt(this.value);
                if (editTextSizeVal) editTextSizeVal.textContent = selectedTextSize + 'px';
                updateStylePreview();
            });
        }

        // Fill opacity slider
        var editFillOpacitySlider = document.getElementById('editTagFillOpacity');
        var editFillOpacityVal = editFillOpacitySlider ? editFillOpacitySlider.parentNode.querySelector('.tag-opacity-value') : null;
        if (editFillOpacitySlider) {
            editFillOpacitySlider.addEventListener('input', function() {
                selectedFillOpacity = parseInt(this.value);
                if (editFillOpacityVal) editFillOpacityVal.textContent = selectedFillOpacity + '%';
                updateStylePreview();
            });
        }

        // Border opacity slider for edit dialog
        var editBorderOpacitySlider = document.getElementById('editBorderOpacitySlider');
        var editBorderOpacityVal = document.getElementById('editBorderOpacityVal');
        if (editBorderOpacitySlider) {
            editBorderOpacitySlider.addEventListener('input', function() {
                selectedBorderOpacity = parseInt(this.value);
                if (editBorderOpacityVal) editBorderOpacityVal.textContent = selectedBorderOpacity + '%';
                updateStylePreview();
            });
        }

        // Icon picker
        var editIconInput = document.getElementById('editTagIconInput');
        var editIconPicker = document.getElementById('editTagIconPicker');
        document.getElementById('btnEditIconPicker').addEventListener('click', function(e) {
            e.stopPropagation();
            editIconPicker.style.display = editIconPicker.style.display === 'none' ? 'block' : 'none';
        });
        editIconPicker.querySelectorAll('.tag-icon-option').forEach(function(opt) {
            opt.addEventListener('click', function() {
                editIconInput.value = opt.dataset.icon;
                selectedIcon = opt.dataset.icon;
                editIconInput.removeAttribute('readonly');
                editIconPicker.querySelectorAll('.tag-icon-option').forEach(function(o) { o.classList.remove('selected'); });
                opt.classList.add('selected');
                editIconPicker.style.display = 'none';
                updateStylePreview();
            });
        });
        editIconInput.addEventListener('input', function() {
            selectedIcon = editIconInput.value;
            updateStylePreview();
        });
        // Close picker on outside click
        document.addEventListener('click', function closeEditIconPicker(e) {
            if (editIconPicker && !editIconPicker.parentNode.contains(e.target)) {
                editIconPicker.style.display = 'none';
            }
        });

        // File picker button — select SVG/ICO from filesystem
        document.getElementById('btnEditIconFile').addEventListener('click', async function(e) {
            e.stopPropagation();
            try {
                var dataUrl = await WailsBridge.pickIconFile();
                if (!dataUrl) return;
                selectedIcon = dataUrl;
                editIconInput.value = '${t("sidebar.custom_icon")}';
                editIconInput.setAttribute('readonly', 'readonly');
                updateStylePreview();
            } catch (err) {
                var msg = (err && (err.message || err + '')) || '未知错误';
                if (msg.indexOf('已取消') !== -1 || msg.indexOf('cancelled') !== -1) return;
                console.error('[PickIconFile]', err);
                App.showToast(t('toast.icon_select_failed') + ': ' + msg, 'error');
            }
        });

        const styleBgInput = document.getElementById('editStyleBgColor');
        const styleTextInput = document.getElementById('editStyleTextColor');
        styleBgInput.addEventListener('input', () => { styleBgColor = styleBgInput.value; updateStylePreview(); });
        styleTextInput.addEventListener('input', () => { styleTextColor = styleTextInput.value; updateStylePreview(); });

        // ============ Avatar panel logic ============
        const avatarBgColorInput = document.getElementById('editAvatarBgColor');
        const avatarTextColorInput = document.getElementById('editAvatarTextColor');
        const avatarPreviewCont = document.getElementById('editAvatarPreview');
        const avatarFileInput = document.getElementById('editAvatarFileInput');
        const avatarUploadArea = document.getElementById('editAvatarUploadArea');

        function getShapeRadius(shape) {
            if (shape === 'sharp') return { outer: 2, thumb: 2 };
            if (shape === 'soft') return { outer: 6, thumb: 4 };
            if (shape === 'round') return { outer: 14, thumb: 10 };
            if (shape === 'pill') return { outer: 99, thumb: 12 };
            return { outer: 14, thumb: 10 };
        }

        function updateAvatarPreview() {
            const name = nameInput.value.trim() || escapeHtmlSidebar(tag.name);
            const showName = document.getElementById('editTagShowName')?.checked !== false;
            const bgColor = avatarBgColorInput.value;
            const textColor = avatarTextColorInput.value;
            const r = getShapeRadius(avatarShape);
            const isImageOnly = !name || !showName;
            const thumbSize = avatarThumbSize;
            const imgUrl = typeof WailsBridge !== 'undefined' && WailsBridge.getAvatarUrl
                ? WailsBridge.getAvatarUrl(avatarDataUrl) : avatarDataUrl;
            const imgHtml = imgUrl
                ? `<img class="av-thumb" src="${imgUrl}" style="width:${thumbSize}px;height:${thumbSize}px;border-radius:${isImageOnly ? r.outer : r.thumb}px;object-fit:cover;" />`
                : `<div class="av-thumb" style="width:${thumbSize}px;height:${thumbSize}px;border-radius:${isImageOnly ? r.outer : r.thumb}px;background:var(--bg-hover);display:flex;align-items:center;justify-content:center;font-size:16px;"><span class="icon icon-image"></span></div>`;
            const labelHtml = isImageOnly ? '' : `<span class="av-label" style="background:${bgColor};color:${textColor};font-size:${avatarTextSize}px;border-radius:0 ${r.outer}px ${r.outer}px 0;">${escapeHtmlSidebar(name)}</span>`;
            avatarPreviewCont.innerHTML = imgHtml + labelHtml;
            avatarPreviewCont.style.borderRadius = r.outer + 'px';
        }

        nameInput.addEventListener('input', updateAvatarPreview);
        avatarBgColorInput.addEventListener('input', updateAvatarPreview);
        avatarTextColorInput.addEventListener('input', updateAvatarPreview);

        bindOptionRow('editTagAvatarShape', v => { avatarShape = v; updateAvatarPreview(); });

        // Avatar thumb size slider
        var editAvatarThumbSizeSlider = document.getElementById('editAvatarThumbSizeSlider');
        var editAvatarThumbSizeVal = document.getElementById('editAvatarThumbSizeVal');
        if (editAvatarThumbSizeSlider) {
            editAvatarThumbSizeSlider.addEventListener('input', function() {
                avatarThumbSize = parseInt(this.value);
                if (editAvatarThumbSizeVal) editAvatarThumbSizeVal.textContent = this.value + 'px';
                updateAvatarPreview();
            });
        }

        // Avatar font size slider
        var editAvatarFontSizeSlider = document.getElementById('editAvatarFontSizeSlider');
        var editAvatarFontSizeVal = document.getElementById('editAvatarFontSizeVal');
        if (editAvatarFontSizeSlider) {
            editAvatarFontSizeSlider.addEventListener('input', function() {
                avatarTextSize = parseInt(this.value);
                if (editAvatarFontSizeVal) editAvatarFontSizeVal.textContent = this.value + 'px';
                updateAvatarPreview();
            });
        }

        if (avatarUploadArea) {
            avatarUploadArea.addEventListener('click', () => avatarFileInput.click());
        }
        if (avatarFileInput) {
            avatarFileInput.addEventListener('change', () => {
                if (!avatarFileInput.files || !avatarFileInput.files[0]) return;
                avatarCropFile = avatarFileInput.files[0];
                openEditAvatarCrop();
            });
        }

        // ============ Crop overlay for edit dialog ============
        function openEditAvatarCrop() {
            const url = URL.createObjectURL(avatarCropFile);
            const oldCrop = document.querySelector('.crop-overlay');
            if (oldCrop) oldCrop.remove();

            const cropOverlay = document.createElement('div');
            cropOverlay.className = 'crop-overlay';
            cropOverlay.innerHTML = `
                <div class="crop-box">
                    <h4>调整裁切范围</h4>
                    <div class="crop-stage" id="cropStage">
                        <img id="cropImg" src="${url}" alt="" />
                        <div class="crop-frame"></div>
                    </div>
                    <div class="crop-controls">
                        <label>缩放</label>
                        <input type="range" id="cropZoom" min="100" max="400" value="100" step="1" />
                    </div>
                    <div class="crop-actions">
                        <button class="btn-secondary" id="btnCropCancel">${t("sidebar.cancel")}</button>
                        <button class="btn-primary" id="btnCropApply">${t("sidebar.confirm_crop")}</button>
                    </div>
                </div>
            `;
            document.body.appendChild(cropOverlay);

            const cropImg = document.getElementById('cropImg');
            const cropStage = document.getElementById('cropStage');
            const zoomRange = document.getElementById('cropZoom');

            cropImg.onload = () => {
                avatarCropImgNatW = cropImg.naturalWidth;
                avatarCropImgNatH = cropImg.naturalHeight;
                const stageW = 300, stageH = 300;
                const scaleToFit = Math.max(stageW / avatarCropImgNatW, stageH / avatarCropImgNatH);
                avatarCropState.scale = scaleToFit;
                avatarCropState.x = (stageW - avatarCropImgNatW * scaleToFit) / 2;
                avatarCropState.y = (stageH - avatarCropImgNatH * scaleToFit) / 2;
                zoomRange.min = Math.round(scaleToFit * 100);
                zoomRange.value = Math.round(scaleToFit * 100);
                applyCropTransform();
            };

            function applyCropTransform() {
                cropImg.style.transform = `translate(${avatarCropState.x}px,${avatarCropState.y}px) scale(${avatarCropState.scale})`;
            }

            function clampCrop() {
                const w = avatarCropImgNatW * avatarCropState.scale;
                const h = avatarCropImgNatH * avatarCropState.scale;
                if (avatarCropState.x > 0) avatarCropState.x = 0;
                if (avatarCropState.y > 0) avatarCropState.y = 0;
                if (avatarCropState.x + w < 300) avatarCropState.x = 300 - w;
                if (avatarCropState.y + h < 300) avatarCropState.y = 300 - h;
            }

            cropStage.addEventListener('mousedown', e => {
                avatarCropIsDrag = true;
                avatarCropDragSX = e.clientX; avatarCropDragSY = e.clientY;
                avatarCropDragOX = avatarCropState.x; avatarCropDragOY = avatarCropState.y;
                e.preventDefault();
            });
            window.addEventListener('mousemove', onCropMove);
            window.addEventListener('mouseup', onCropUp);
            cropStage.addEventListener('touchstart', e => {
                const t = e.touches[0];
                avatarCropIsDrag = true;
                avatarCropDragSX = t.clientX; avatarCropDragSY = t.clientY;
                avatarCropDragOX = avatarCropState.x; avatarCropDragOY = avatarCropState.y;
                e.preventDefault();
            }, { passive: false });
            window.addEventListener('touchmove', onCropTouchMove, { passive: false });
            window.addEventListener('touchend', onCropUp);

            function onCropMove(e) {
                if (!avatarCropIsDrag) return;
                avatarCropState.x = avatarCropDragOX + (e.clientX - avatarCropDragSX);
                avatarCropState.y = avatarCropDragOY + (e.clientY - avatarCropDragSY);
                clampCrop();
                applyCropTransform();
            }
            function onCropTouchMove(e) {
                if (!avatarCropIsDrag) return;
                const t = e.touches[0];
                avatarCropState.x = avatarCropDragOX + (t.clientX - avatarCropDragSX);
                avatarCropState.y = avatarCropDragOY + (t.clientY - avatarCropDragSY);
                clampCrop();
                applyCropTransform();
            }
            function onCropUp() { avatarCropIsDrag = false; }

            zoomRange.addEventListener('input', () => {
                const z = parseInt(zoomRange.value) / 100;
                const prev = avatarCropState.scale;
                const cx = 150, cy = 150;
                avatarCropState.x = cx - (cx - avatarCropState.x) * (z / prev);
                avatarCropState.y = cy - (cy - avatarCropState.y) * (z / prev);
                avatarCropState.scale = z;
                clampCrop();
                applyCropTransform();
            });

            document.getElementById('btnCropCancel').addEventListener('click', () => cleanupCrop());
            document.getElementById('btnCropApply').addEventListener('click', () => {
                const sx = -avatarCropState.x / avatarCropState.scale;
                const sy = -avatarCropState.y / avatarCropState.scale;
                const sw = 300 / avatarCropState.scale;
                const sh = 300 / avatarCropState.scale;
                const canvas = document.createElement('canvas');
                canvas.width = 256; canvas.height = 256;
                const ctx = canvas.getContext('2d');
                ctx.drawImage(cropImg, sx, sy, sw, sh, 0, 0, 256, 256);
                // 转为 Blob 保存，用 blob URL 预览
                canvas.toBlob((blob) => {
                    avatarCropBlob = blob;
                    avatarDataUrl = URL.createObjectURL(blob);
                    const area = document.getElementById('editAvatarUploadArea');
                    if (area) {
                        area.innerHTML = `<img src="${avatarDataUrl}" alt="avatar" /><div class="avatar-upload-cam"><span class="icon icon-image"></span></div>`;
                        area.classList.add('has-image');
                    }
                    updateAvatarPreview();
                }, 'image/jpeg', 0.85);
                cleanupCrop();
            });

            function cleanupCrop() {
                window.removeEventListener('mousemove', onCropMove);
                window.removeEventListener('mouseup', onCropUp);
                window.removeEventListener('touchmove', onCropTouchMove);
                window.removeEventListener('touchend', onCropUp);
                cropOverlay.remove();
                URL.revokeObjectURL(url);
            }

            cropOverlay.addEventListener('click', (e) => {
                if (e.target === cropOverlay) cleanupCrop();
            });
        }

        // ============ Save ============
        document.getElementById('btnSaveEditTag').addEventListener('click', async () => {
            const name = nameInput.value.trim();
            const showName = document.getElementById('editTagShowName')?.checked !== false;

            if (currentType === 'avatar' && !avatarCropBlob && !avatarDataUrl) {
                App.showToast(t('toast.avatar_image_needed'), 'warning'); return;
            }
            if (currentType === 'html') {
                htmlCode = document.getElementById('editHtmlCode')?.value || '';
                htmlWidth = parseInt(document.getElementById('editHtmlWidth')?.value) || 120;
                htmlHeight = parseInt(document.getElementById('editHtmlHeight')?.value) || 40;
            }

            try {
                const editParentId = document.getElementById('editTagParent')?.value || null;
                const updatedTag = { ...tag }; // 保留原标签所有属性
                updatedTag.name = name;
                updatedTag.showName = showName;
                updatedTag.tagType = currentType;
                updatedTag.parentId = editParentId || null;

                if (currentType === 'avatar') {
                    if (avatarCropBlob) {
                        const base64 = await new Promise((resolve, reject) => {
                            const reader = new FileReader();
                            reader.onload = () => resolve(reader.result);
                            reader.onerror = reject;
                            reader.readAsDataURL(avatarCropBlob);
                        });
                        if (typeof WailsBridge !== 'undefined' && WailsBridge.saveAvatar) {
                            updatedTag.avatarData = await WailsBridge.saveAvatar(base64);
                        } else {
                            updatedTag.avatarData = base64;
                        }
                    } else {
                        updatedTag.avatarData = avatarDataUrl;
                    }
                    updatedTag.color = avatarTextColorInput.value;
                    updatedTag.bgColor = avatarBgColorInput.value;
                    updatedTag.shape = avatarShape;
                    updatedTag.avatarTextSize = avatarTextSize;
                    updatedTag.avatarThumbSize = avatarThumbSize;
                    updatedTag.fillStyle = 'solid';
                    updatedTag.borderStyle = 'solid';
                    updatedTag.size = 'md';
                    updatedTag.weight = 'regular';
                    updatedTag.icon = '';
                    updatedTag.textColor = avatarTextColorInput.value;
                } else if (currentType === 'html') {
                    updatedTag.htmlCode = htmlCode;
                    updatedTag.htmlWidth = htmlWidth;
                    updatedTag.htmlHeight = htmlHeight;
                    updatedTag.fillStyle = 'solid';
                    updatedTag.borderStyle = 'solid';
                    updatedTag.icon = '';
                    updatedTag.avatarData = '';
                } else {
                    updatedTag.color = styleBgColor;
                    updatedTag.fillStyle = selectedFillStyle;
                    updatedTag.fillOpacity = selectedFillOpacity;
                    updatedTag.shape = selectedShape;
                    updatedTag.icon = selectedIcon;
                    updatedTag.borderStyle = selectedBorderStyle;
                    updatedTag.borderOpacity = selectedBorderOpacity;
                    updatedTag.size = selectedSize;
                    updatedTag.textSize = selectedTextSize;
                    updatedTag.weight = selectedWeight;
                    const hasIcon = !!selectedIcon;
                    const isIconOnly = hasIcon && !name;
                    updatedTag.iconOnly = isIconOnly;
                    updatedTag.iconOnlySize = hasIcon ? iconOnlySize : undefined;
                    updatedTag.bgColor = styleBgColor;
                    updatedTag.textColor = styleTextColor;
                    updatedTag.avatarData = '';
                }

                await Storage.updateTag(updatedTag);
                closeDialog();
                await refreshTagTree();
                App.showToast(t('toast.tag_updated_with_name').replace('{name}', name), 'success');
            } catch (err) {
                App.showToast(t('toast.update_tag_failed') + ': ' + err.message, 'error');
            }
        });

        // ============ Cancel ============
        content.querySelectorAll('.btn-cancel-dialog').forEach(btn => {
            btn.addEventListener('click', () => closeDialog());
        });
        overlay.addEventListener('click', (e) => {
            if (e.target === overlay) closeDialog();
        });

        // ============ HTML code live preview ============
        const editHtmlCodeEl = document.getElementById('editHtmlCode');
        const editHtmlPreviewEl = document.getElementById('editHtmlPreviewBox');
        if (editHtmlCodeEl && editHtmlPreviewEl) {
            const editPreviewScopeId = 'edit-html-preview';
            const editWidthInput = document.getElementById('editHtmlWidth');
            const editHeightInput = document.getElementById('editHtmlHeight');
            const updateEditHtmlPreview = () => {
                const code = editHtmlCodeEl.value;
                if (!code.trim()) {
                    editHtmlPreviewEl.removeAttribute('data-tag-scope');
                    editHtmlPreviewEl.innerHTML = '';
                    editHtmlPreviewEl.style.width = '';
                    editHtmlPreviewEl.style.height = '';
                    return;
                }
                const w = editWidthInput ? parseInt(editWidthInput.value) || 120 : 120;
                const h = editHeightInput ? parseInt(editHeightInput.value) || 40 : 40;
                editHtmlPreviewEl.style.width = w + 'px';
                editHtmlPreviewEl.style.height = h + 'px';
                applyScopeToContainer(editHtmlPreviewEl, code, editPreviewScopeId);
            };
            let editHtmlPreviewTimer = null;
            editHtmlCodeEl.addEventListener('input', () => {
                clearTimeout(editHtmlPreviewTimer);
                editHtmlPreviewTimer = setTimeout(updateEditHtmlPreview, 300);
            });
            const editRescalePreview = () => {
                const inner = editHtmlPreviewEl.querySelector('.html-preview-inner');
                if (!inner) return;
                // 重置 transform 才能测量真实尺寸
                inner.style.transform = '';
                requestAnimationFrame(() => {
                    const rect = inner.getBoundingClientRect();
                    const cw = editHtmlPreviewEl.clientWidth;
                    const ch = editHtmlPreviewEl.clientHeight;
                    if (!cw || !ch) return;
                    const nw = rect.width || cw;
                    const nh = rect.height || ch;
                    const scale = Math.min(cw / nw, ch / nh, 1);
                    inner.style.transform = `scale(${scale})`;
                    const scaledW = nw * scale;
                    const scaledH = nh * scale;
                    inner.style.left = ((cw - scaledW) / 2) + 'px';
                    inner.style.top = ((ch - scaledH) / 2) + 'px';
                });
            };
            if (editWidthInput) {
                editWidthInput.addEventListener('input', () => {
                    editHtmlPreviewEl.style.width = (parseInt(editWidthInput.value) || 120) + 'px';
                    editRescalePreview();
                });
            }
            if (editHeightInput) {
                editHeightInput.addEventListener('input', () => {
                    editHtmlPreviewEl.style.height = (parseInt(editHeightInput.value) || 40) + 'px';
                    editRescalePreview();
                });
            }
            updateEditHtmlPreview();
        }

        setTimeout(() => nameInput.focus(), 100);
    }

    // scopeHtmlCode: 对 HTML 代码的 style 标签做 scope 隔离 + 过滤全局选择器
    // 同时创建 inner 容器 + 缩放逻辑，与标签栏 createTagItem 保持一致
    function applyScopeToContainer(container, htmlCode, scopeId) {
        container.setAttribute('data-tag-scope', scopeId);
        // 先对完整 HTML 做 URL 替换（<img src>, <image href> + CSS url()）
        let processedCode = (typeof WailsBridge !== 'undefined' && WailsBridge.fixRelativeUrls)
            ? WailsBridge.fixRelativeUrls(htmlCode || '') : (htmlCode || '');
        let scopedHtml = processedCode.replace(/<style([^>]*)>/g, (_, attrs) => {
            return '<style' + attrs + ' data-scope="' + scopeId + '">';
        });
        // 创建 inner 容器（与标签栏一致的双层结构）
        let inner = container.querySelector('.html-preview-inner');
        if (!inner) {
            inner = document.createElement('div');
            inner.className = 'html-preview-inner';
            inner.style.cssText = 'position:absolute;top:0;left:0;transform-origin:0 0;';
            container.style.position = 'relative';
            container.innerHTML = '';
            container.appendChild(inner);
        }
        // 重置 transform 才能正确测量自然尺寸
        inner.style.transform = '';
        inner.innerHTML = scopedHtml;
        inner.querySelectorAll('style[data-scope]').forEach(styleEl => {
            const raw = styleEl.textContent;
            if (!raw) return;
            const scoped = raw.replace(/([^{}]*\{)/g, (rule) => {
                const trimmed = rule.trim();
                if (/^@|^\d+(\.\d+)?%|^(from|to)\b/i.test(trimmed)) return rule;
                if (/\b(html|body|:root)\b|\*/.test(trimmed)) return '';
                return rule.replace(/(^|,)\s*/g, (sep) => {
                    return sep + '[data-tag-scope="' + scopeId + '"] ';
                });
            });
            styleEl.textContent = (typeof WailsBridge !== 'undefined' && WailsBridge.fixRelativeUrls)
                ? WailsBridge.fixRelativeUrls(scoped) : scoped;
            styleEl.removeAttribute('data-scope');
        });
        inner.querySelectorAll('script').forEach(s => {
            try { eval(s.textContent); } catch (_) {}
        });

        const doScale = () => {
            const rect = inner.getBoundingClientRect();
            const cw = container.clientWidth;
            const ch = container.clientHeight;
            if (!cw || !ch) return;
            const nw = rect.width || cw;
            const nh = rect.height || ch;
            const scale = Math.min(cw / nw, ch / nh, 1);
            inner.style.transform = `scale(${scale})`;
            const scaledW = nw * scale;
            const scaledH = nh * scale;
            inner.style.left = ((cw - scaledW) / 2) + 'px';
            inner.style.top = ((ch - scaledH) / 2) + 'px';
        };
        // 先立即缩放一次，再等图片加载后重试
        requestAnimationFrame(() => {
            doScale();
            // 等所有图片加载完成后再次缩放
            const imgs = inner.querySelectorAll('img');
            if (imgs.length > 0) {
                let pending = imgs.length;
                const onLoad = () => { pending--; if (pending === 0) doScale(); };
                imgs.forEach(img => {
                    if (img.complete) { pending--; } else { img.addEventListener('load', onLoad, { once: true }); img.addEventListener('error', onLoad, { once: true }); }
                });
                if (pending === 0) doScale();
            }
        });
    }

    function escapeHtmlSidebar(str) {
        const div = document.createElement('div');
        div.textContent = str;
        return div.innerHTML;
    }

    // ==================== Folder Drag Engine (Ghost Placeholder) ====================
    //
    // Same pattern as tag drag: pointer-based ghost placeholder for vertical list.
    // In edit mode, pointerdown on a folder's drag handle pulls it out of flow
    // (position:absolute), a ghost bar shows the insertion point, and other folders
    // collapse/expand around it. On drop: FLIP animation snaps everything into place.

    const FOLDER_DRAG = {
        settleMs: 200
    };

    function initFolderDrag(container) {
        container.addEventListener('pointerdown', (e) => {
            if (!isEditingFolderOrder) return;
            const handle = e.target.closest('.tree-drag-handle');
            if (!handle) return;
            const folderEl = handle.closest('.tree-node[data-is-root="true"]');
            if (!folderEl) return;

            e.preventDefault();
            startFolderDrag(e, container, folderEl);
        });
    }

    function startFolderDrag(e, container, folderEl) {
        if (folderDrag && folderDrag.active) return;

        const rootNodes = Array.from(container.querySelectorAll('.tree-node[data-is-root="true"]'));
        if (rootNodes.length < 2) return;

        const containerRect = container.getBoundingClientRect();
        const fromIndex = parseInt(folderEl.dataset.folderIndex);

        // Snapshot all root node positions
        const positions = new Array(rootNodes.length);
        for (let i = 0; i < rootNodes.length; i++) {
            const r = rootNodes[i].getBoundingClientRect();
            positions[i] = {
                el: rootNodes[i],
                restX: r.left - containerRect.left,
                restY: r.top - containerRect.top,
                width: r.width,
                height: r.height
            };
        }

        // Create ghost placeholder bar (full width, fixed height)
        const ghost = document.createElement('div');
        ghost.className = 'folder-ghost-placeholder';
        ghost.style.width = positions[fromIndex].width + 'px';
        container.insertBefore(ghost, folderEl);

        // Pull folder out of flow
        const folderRect = folderEl.getBoundingClientRect();
        const offsetX = e.clientX - folderRect.left;
        const offsetY = e.clientY - folderRect.top;

        // Ensure container is the positioning anchor
        container.style.position = 'relative';

        folderEl.style.position = 'absolute';
        folderEl.style.zIndex = '20';
        folderEl.style.pointerEvents = 'none';
        folderEl.style.transition = 'none';
        folderEl.style.boxShadow = '0 6px 20px rgba(0,0,0,0.35)';
        folderEl.style.width = folderRect.width + 'px';
        // Place at natural position, then dragLoop will move it to follow the pointer
        folderEl.style.left = (folderRect.left - containerRect.left) + 'px';
        folderEl.style.top = (folderRect.top - containerRect.top) + 'px';

        // Lift overflow clipping so the dragged folder can move above the container's top edge
        const savedOverflow = container.style.overflow;
        container.style.overflow = 'visible';

        folderDrag = {
            active: true,
            didMove: false,
            container,
            items: positions,
            fromIndex,
            dragEl: folderEl,
            dragWidth: folderRect.width,
            dragHeight: folderRect.height,
            offsetX,
            offsetY,
            ghost,
            pointerX: e.clientX,
            pointerY: e.clientY,
            raf: null,
            lastInsertBefore: folderEl,
            savedOverflow
        };

        document.addEventListener('pointermove', onFolderPointerMove, { passive: false });
        document.addEventListener('pointerup', onFolderPointerUp, { once: true });
        document.addEventListener('pointercancel', onFolderPointerUp, { once: true });

        container.style.touchAction = 'none';
        container.classList.add('is-dragging');

        folderDrag.raf = requestAnimationFrame(dragFolderLoop);
    }

    function onFolderPointerMove(e) {
        if (!folderDrag || !folderDrag.active) return;
        e.preventDefault();

        const dx = e.clientX - folderDrag.pointerX;
        const dy = e.clientY - folderDrag.pointerY;
        if (Math.abs(dx) + Math.abs(dy) > 3) {
            folderDrag.didMove = true;
        }

        folderDrag.pointerX = e.clientX;
        folderDrag.pointerY = e.clientY;
    }

    // Returns the first folder node that the ghost should be inserted before,
    // or null meaning "append ghost at end of container".
    // Uses sorted visual positions so logic matches onFolderPointerUp exactly.
    function computeFolderInsertBefore(drag) {
        const cr = drag.container.getBoundingClientRect();
        const dr = drag.dragEl.getBoundingClientRect();
        const dcy = dr.top + dr.height * 0.5 - cr.top;

        const nodes = Array.from(drag.container.querySelectorAll('.tree-node[data-is-root="true"]'));
        const others = [];
        for (const node of nodes) {
            if (node === drag.dragEl) continue;
            const r = node.getBoundingClientRect();
            others.push({
                el: node,
                cy: r.top + r.height * 0.5 - cr.top
            });
        }
        others.sort((a, b) => a.cy - b.cy);

        for (let i = 0; i < others.length; i++) {
            if (dcy < others[i].cy) return others[i].el;
        }
        return null; // append at end
    }

    function dragFolderLoop() {
        if (!folderDrag || !folderDrag.active) return;

        const drag = folderDrag;
        const cr = drag.container.getBoundingClientRect();

        // Move dragged folder (position:absolute) to follow pointer, preserving grab offset
        drag.dragEl.style.left = (drag.pointerX - cr.left - drag.offsetX) + 'px';
        drag.dragEl.style.top = (drag.pointerY - cr.top - drag.offsetY) + 'px';

        // Move ghost to current insertion point
        if (drag.didMove) {
            const beforeEl = computeFolderInsertBefore(drag);
            if (beforeEl !== drag.lastInsertBefore) {
                drag.lastInsertBefore = beforeEl;
                if (beforeEl) {
                    drag.container.insertBefore(drag.ghost, beforeEl);
                } else {
                    drag.container.appendChild(drag.ghost);
                }
            }
        }

        drag.raf = requestAnimationFrame(dragFolderLoop);
    }

    function onFolderPointerUp(e) {
        document.removeEventListener('pointermove', onFolderPointerMove);
        document.removeEventListener('pointerup', onFolderPointerUp);
        document.removeEventListener('pointercancel', onFolderPointerUp);

        if (!folderDrag || !folderDrag.active) return;

        const drag = folderDrag;
        cancelAnimationFrame(drag.raf);
        drag.active = false;

        if (drag.container) {
            drag.container.style.touchAction = '';
        }

        // Compute new index from Y position using origIdx pattern (same as tag drag)
        const cr = drag.container.getBoundingClientRect();
        const dr = drag.dragEl.getBoundingClientRect();
        const dcy = dr.top + dr.height * 0.5 - cr.top;

        const nodes = Array.from(drag.container.querySelectorAll('.tree-node[data-is-root="true"]'));
        const others = [];
        for (const node of nodes) {
            if (node === drag.dragEl) continue;
            const r = node.getBoundingClientRect();
            others.push({
                origIdx: parseInt(node.dataset.folderIndex),
                cy: r.top + r.height * 0.5 - cr.top
            });
        }
        others.sort((a, b) => a.cy - b.cy);

        let insertPos = others.length;
        for (let i = 0; i < others.length; i++) {
            if (dcy < others[i].cy) { insertPos = i; break; }
        }

        let newIndex;
        if (insertPos >= others.length) {
            newIndex = nodes.length - 1; // dragged node is still in nodes, so length is N
        } else {
            newIndex = others[insertPos].origIdx;
            if (newIndex > drag.fromIndex) newIndex--;
        }
        newIndex = Math.max(0, Math.min(nodes.length - 1, newIndex));

        if (newIndex !== drag.fromIndex) {
            const [moved] = folderRoots.splice(drag.fromIndex, 1);
            folderRoots.splice(newIndex, 0, moved);
            saveFolderOrder();
        }

        animateFolderSettle(drag);
    }

    function animateFolderSettle(drag) {
        // 1. Record live visual positions
        const cr = drag.container.getBoundingClientRect();
        const entries = [];
        const allNodes = Array.from(drag.container.querySelectorAll('.tree-node[data-is-root="true"]'));
        for (const el of allNodes) {
            const r = el.getBoundingClientRect();
            entries.push({ el, prevX: r.left - cr.left, prevY: r.top - cr.top });
        }

        // 2. Remove ghost
        if (drag.ghost && drag.ghost.parentNode) {
            drag.ghost.parentNode.removeChild(drag.ghost);
            drag.ghost = null;
        }

        // 2b. Restore overflow and hover
        drag.container.style.overflow = drag.savedOverflow || '';
        drag.container.classList.remove('is-dragging');

        // 3. Restore dragged folder to normal flow
        drag.dragEl.style.position = '';
        drag.dragEl.style.left = '';
        drag.dragEl.style.top = '';
        drag.dragEl.style.zIndex = '';
        drag.dragEl.style.pointerEvents = '';
        drag.dragEl.style.boxShadow = '';
        drag.dragEl.style.width = '';
        drag.dragEl.style.transition = 'none';
        drag.dragEl.style.transform = '';

        // 4. Reorder DOM to match final folderRoots order
        const pathToEl = {};
        for (const item of drag.items) {
            pathToEl[item.el.dataset.path] = item.el;
        }
        const frag = document.createDocumentFragment();
        for (const root of folderRoots) {
            const el = pathToEl[root.path];
            if (el) frag.appendChild(el);
        }
        drag.container.appendChild(frag);

        // Update dataset.folderIndex so consecutive drags see correct positions
        const reorderedNodes = Array.from(drag.container.querySelectorAll('.tree-node[data-is-root="true"]'));
        for (let i = 0; i < reorderedNodes.length; i++) {
            reorderedNodes[i].dataset.folderIndex = i;
        }

        drag.container.offsetHeight; // force layout

        // 5. Measure new positions & invert
        for (const entry of entries) {
            const r = entry.el.getBoundingClientRect();
            const newX = r.left - cr.left;
            const newY = r.top - cr.top;
            const dx = entry.prevX - newX;
            const dy = entry.prevY - newY;

            if (Math.abs(dx) > 0.5 || Math.abs(dy) > 0.5) {
                entry.el.style.transition = 'none';
                entry.el.style.transform = `translate(${dx}px,${dy}px)`;
            }
        }

        // 6. Play
        requestAnimationFrame(() => {
            for (const entry of entries) {
                entry.el.style.transition = `transform ${FOLDER_DRAG.settleMs}ms ease-out`;
                entry.el.style.transform = '';
            }

            setTimeout(() => {
                for (const entry of entries) {
                    entry.el.style.transition = '';
                    entry.el.style.transform = '';
                }
                folderDrag = null;
            }, FOLDER_DRAG.settleMs + 50);
        });
    }

    // ==================== API 配置 ====================

    async function refreshApiConfigSelect() {
        try {
            if (typeof Storage !== 'undefined' && Storage.getAllApiConfigs) {
                apiConfigs = await Storage.getAllApiConfigs();
            }
            // 从服务器同步当前活跃配置 ID
            if (typeof Storage !== 'undefined' && Storage.getSetting) {
                const savedId = await Storage.getSetting('activeApiConfigId', null);
                if (savedId && apiConfigs.some(c => c.id === savedId)) {
                    currentApiConfigId = savedId;
                }
            }
            renderApiConfigSelect();
            // 同步刷新右侧详情面板的配置选择器
            if (typeof DetailPanel !== 'undefined' && DetailPanel.refreshDetailApiConfigSelect) {
                DetailPanel.refreshDetailApiConfigSelect();
            }
        } catch (err) {
            console.error('[Sidebar] 刷新 API 配置失败:', err);
        }
    }

    function renderApiConfigSelect() {
        if (!apiConfigSelect) return;

        apiConfigSelect.innerHTML = '<option value="">' + t('sidebar.select_config') + '</option>';
        for (const config of apiConfigs) {
            const option = document.createElement('option');
            option.value = config.id;
            option.textContent = config.name + (config.isDefault ? t('sidebar.default_suffix') : '');
            apiConfigSelect.appendChild(option);
        }

        if (currentApiConfigId && apiConfigs.some(c => c.id === currentApiConfigId)) {
            apiConfigSelect.value = currentApiConfigId;
            loadApiConfigToForm(currentApiConfigId);
        }
    }

    function onApiConfigSelectChange() {
        const id = apiConfigSelect.value;
        currentApiConfigId = id || null;
        // 将当前选中设为活跃配置，供 API 调用时使用
        if (typeof Storage !== 'undefined' && Storage.setSetting) {
            Storage.setSetting('activeApiConfigId', id || null);
        }
        cachedModelList = []; // 切换配置时清空模型缓存
        document.getElementById('modelDropdownList').style.display = 'none';
        if (id) {
            loadApiConfigToForm(id);
        } else {
            clearApiConfigForm();
        }
    }

    function loadApiConfigToForm(id) {
        const config = apiConfigs.find(c => c.id === id);
        if (!config) return;

        document.getElementById('apiName').value = config.name || '';
        document.getElementById('apiBaseUrl').value = config.baseUrl || '';
        document.getElementById('apiKey').value = config.apiKey || '';
        document.getElementById('apiModel').value = config.model || '';
        document.getElementById('apiTemperature').value = config.temperature || 0.7;
        document.getElementById('tempValue').textContent = config.temperature || 0.7;
        document.getElementById('apiMaxTokens').value = config.maxTokens || 2048;
        document.getElementById('apiTimeout').value = config.timeout || 120;
        document.getElementById('apiSystemPrompt').value = config.systemPrompt || '';
        document.getElementById('apiUserPrompt').value = config.userPrompt || '';
        document.getElementById('apiStripThinking').checked = config.stripThinking || false;
        document.getElementById('apiIsDefault').checked = config.isDefault || false;

        if (config.proxy) {
            document.getElementById('apiProxyEnabled').checked = config.proxy.enabled || false;
            document.getElementById('apiProxyHost').value = config.proxy.host || '';
            document.getElementById('apiProxyPort').value = config.proxy.port || '';
        }
    }

    function clearApiConfigForm() {
        document.getElementById('apiName').value = '';
        document.getElementById('apiBaseUrl').value = '';
        document.getElementById('apiKey').value = '';
        document.getElementById('apiModel').value = '';
        document.getElementById('apiTemperature').value = 0.7;
        document.getElementById('tempValue').textContent = '0.7';
        document.getElementById('apiMaxTokens').value = 2048;
        document.getElementById('apiTimeout').value = 120;
        document.getElementById('apiSystemPrompt').value = '';
        document.getElementById('apiUserPrompt').value = '';
        document.getElementById('apiStripThinking').checked = false;
        document.getElementById('apiIsDefault').checked = false;
        document.getElementById('apiProxyEnabled').checked = false;
        document.getElementById('apiProxyHost').value = '';
        document.getElementById('apiProxyPort').value = '';
    }

    function newApiConfig() {
        currentApiConfigId = null;
        apiConfigSelect.value = '';
        cachedModelList = [];
        document.getElementById('modelDropdownList').style.display = 'none';
        clearApiConfigForm();
        App.showToast(t('toast.api_fill_config'), 'info');
    }

    async function saveApiConfig() {
        const config = {
            id: currentApiConfigId || 'api_' + Date.now(),
            name: document.getElementById('apiName').value.trim(),
            baseUrl: document.getElementById('apiBaseUrl').value.trim(),
            apiKey: document.getElementById('apiKey').value.trim(),
            model: document.getElementById('apiModel').value.trim(),
            temperature: parseFloat(document.getElementById('apiTemperature').value) || 0.7,
            maxTokens: parseInt(document.getElementById('apiMaxTokens').value) || 2048,
            timeout: parseInt(document.getElementById('apiTimeout').value) || 120,
            systemPrompt: document.getElementById('apiSystemPrompt').value.trim(),
            userPrompt: document.getElementById('apiUserPrompt').value.trim(),
            stripThinking: document.getElementById('apiStripThinking').checked,
            isDefault: document.getElementById('apiIsDefault').checked,
            proxy: {
                enabled: document.getElementById('apiProxyEnabled').checked,
                protocol: 'http',
                host: document.getElementById('apiProxyHost').value.trim(),
                port: parseInt(document.getElementById('apiProxyPort').value) || 0
            },
            createdAt: currentApiConfigId ? undefined : new Date().toISOString()
        };

        if (!config.name) {
            App.showToast(t('sidebar.enter_config_name'), 'warning');
            return;
        }
        if (!config.baseUrl) {
            App.showToast(t('sidebar.enter_api_url'), 'warning');
            return;
        }

        // 刷新配置列表确保最新数据
        if (typeof Storage !== 'undefined' && Storage.getAllApiConfigs) {
            apiConfigs = await Storage.getAllApiConfigs();
        }

        // 检查同名配置（按配置名称，非模型名称）
        let isOverwrite = false;
        const sameNameConfig = apiConfigs.find(c => c.name === config.name && c.id !== config.id);
        if (sameNameConfig) {
            if (!confirm(t('sidebar.config_exists_overwrite').replace('{name}', config.name))) return;
            config.id = sameNameConfig.id;
            config.createdAt = sameNameConfig.createdAt;
            isOverwrite = true;
        }

        try {
            if (currentApiConfigId || isOverwrite) {
                if (typeof Storage !== 'undefined' && Storage.updateApiConfig) {
                    await Storage.updateApiConfig(config);
                }
            } else {
                if (typeof Storage !== 'undefined' && Storage.addApiConfig) {
                    await Storage.addApiConfig(config);
                }
            }
            currentApiConfigId = config.id;
            await refreshApiConfigSelect();
            App.showToast(t('toast.api_saved'), 'success');
        } catch (err) {
            App.showToast(t('toast.save_failed') + ': ' + err.message, 'error');
        }
    }

    async function deleteCurrentApiConfig() {
        if (!currentApiConfigId) {
            App.showToast(t('toast.api_select_to_delete'), 'warning');
            return;
        }

        if (!confirm(t('sidebar.confirm_delete_api'))) return;

        try {
            if (typeof Storage !== 'undefined' && Storage.deleteApiConfig) {
                await Storage.deleteApiConfig(currentApiConfigId);
            }
            currentApiConfigId = null;
            await refreshApiConfigSelect();
            App.showToast(t('toast.api_deleted'), 'success');
        } catch (err) {
            App.showToast(t('toast.delete_failed') + ': ' + err.message, 'error');
        }
    }

    async function cloneCurrentApiConfig() {
        if (!currentApiConfigId) {
            App.showToast(t('toast.api_select_to_clone'), 'warning');
            return;
        }

        const sourceConfig = apiConfigs.find(c => c.id === currentApiConfigId);
        if (!sourceConfig) {
            App.showToast(t('toast.api_config_not_found'), 'error');
            return;
        }

        // 构建克隆配置（新ID + 名称加"副本"后缀）
        const cloned = {
            ...sourceConfig,
            id: 'api_' + Date.now(),
            name: (sourceConfig.name || '') + ' - 副本',
            isDefault: false,
            createdAt: new Date().toISOString()
        };

        try {
            if (typeof Storage !== 'undefined' && Storage.addApiConfig) {
                await Storage.addApiConfig(cloned);
            }
            // 刷新下拉列表并选中新配置
            await refreshApiConfigSelect();
            apiConfigSelect.value = cloned.id;
            currentApiConfigId = cloned.id;
            if (typeof Storage !== 'undefined' && Storage.setSetting) {
                Storage.setSetting('activeApiConfigId', cloned.id);
            }
            loadApiConfigToForm(cloned.id);
            App.showToast(t('toast.api_config_cloned'), 'success');
        } catch (err) {
            App.showToast(t('toast.clone_failed') + ': ' + err.message, 'error');
        }
    }

    async function renameCurrentApiConfig() {
        if (!currentApiConfigId) {
            App.showToast(t('sidebar.select_config_to_rename'), 'warning');
            return;
        }
        const input = document.getElementById('apiName');
        if (input) {
            input.focus();
            input.select();
        }
    }

    async function testApiConfig() {
        const config = {
            baseUrl: document.getElementById('apiBaseUrl').value.trim(),
            apiKey: document.getElementById('apiKey').value.trim(),
            proxyEnabled: document.getElementById('apiProxyEnabled').checked,
            proxyProtocol: 'http',
            proxyHost: document.getElementById('apiProxyHost').value.trim(),
            proxyPort: parseInt(document.getElementById('apiProxyPort').value) || 0
        };

        if (!config.baseUrl) {
            App.showToast(t('toast.api_fill_url'), 'warning');
            return;
        }

        document.getElementById('btnTestApiConfig').innerHTML = '<span class="icon icon-loading"></span> ' + t('sidebar.connecting');
        document.getElementById('btnTestApiConfig').disabled = true;

        try {
            const result = await ApiService.testConnection(config);
            const modelInput = document.getElementById('apiModel');
            const dropdown = document.getElementById('modelDropdownList');

            if (result.success && result.models.length > 0) {
                // 缓存模型列表供后续筛选使用
                cachedModelList = result.models;
                const modelInput = document.getElementById('apiModel');

                // 如果模型字段为空，自动填入第一个
                if (!modelInput.value.trim()) {
                    modelInput.value = result.models[0];
                }

                // 渲染筛选后的下拉（无筛选 = 显示全部）
                renderModelDropdown('');

                App.showToast(`✅ 连接成功！检测到 ${result.models.length} 个模型`, 'success');
            } else {
                document.getElementById('modelDropdownList').style.display = 'none';
                App.showToast(t('toast.api_test_no_models'), 'error');
            }
        } catch (err) {
            document.getElementById('modelDropdownList').style.display = 'none';
            App.showToast(t('toast.connect_failed') + ': ' + err.message, 'error');
        } finally {
            document.getElementById('btnTestApiConfig').innerHTML = '<span class="icon icon-plug"></span> ' + t('panel.test_api');
            document.getElementById('btnTestApiConfig').disabled = false;
        }
    }

    function renderModelDropdown(filterText) {
        const modelInput = document.getElementById('apiModel');
        const dropdown = document.getElementById('modelDropdownList');
        if (!dropdown) return;

        // 筛选
        const filtered = filterText
            ? cachedModelList.filter(m => m.toLowerCase().includes(filterText.toLowerCase()))
            : cachedModelList;

        if (filtered.length === 0 && filterText) {
            // 无匹配：显示自定义入口
            dropdown.innerHTML = '<div class="model-dropdown-item" data-model="__custom__" style="color:var(--accent);font-family:var(--font-sans);"><span class="icon icon-edit"></span> ' + t('sidebar.use_model').replace('{name}', escapeHtmlSidebar(filterText)) + '</div>';
            dropdown.style.display = 'block';
            bindModelDropdownClicks(modelInput, dropdown);
            return;
        }

        if (filtered.length === 0) {
            dropdown.style.display = 'none';
            return;
        }

        let html = '';
        for (const m of filtered) {
            html += `<div class="model-dropdown-item" data-model="${escapeHtmlSidebar(m)}">${escapeHtmlSidebar(m)}</div>`;
        }
        html += `<div class="model-dropdown-item" data-model="__custom__" style="color:var(--accent);font-family:var(--font-sans);"><span class="icon icon-edit"></span> 使用自定义模型...</div>`;
        dropdown.innerHTML = html;
        dropdown.style.display = 'block';
        bindModelDropdownClicks(modelInput, dropdown);
    }

    function bindModelDropdownClicks(modelInput, dropdown) {
        dropdown.querySelectorAll('.model-dropdown-item').forEach(item => {
            item.addEventListener('click', () => {
                const model = item.dataset.model;
                if (model === '__custom__') {
                    modelInput.value = '';
                    modelInput.focus();
                    modelInput.placeholder = '输入自定义模型名称...';
                } else {
                    modelInput.value = model;
                }
                dropdown.style.display = 'none';
            });
        });
    }

    function initModelDropdown() {
        const modelInput = document.getElementById('apiModel');
        const dropdown = document.getElementById('modelDropdownList');
        if (!modelInput || !dropdown) return;

        // 点击输入框 → 显示全部模型列表（无筛选）
        modelInput.addEventListener('click', () => {
            if (cachedModelList.length > 0) {
                renderModelDropdown('');
            }
        });

        // 输入时 → 实时筛选
        modelInput.addEventListener('input', () => {
            const val = modelInput.value.trim();
            if (cachedModelList.length > 0) {
                renderModelDropdown(val);
            }
        });

        // 按 Esc 关闭下拉
        modelInput.addEventListener('keydown', (e) => {
            if (e.key === 'Escape') {
                dropdown.style.display = 'none';
            }
        });

        // 点击外部关闭下拉
        document.addEventListener('click', (e) => {
            if (!dropdown.contains(e.target) && e.target !== modelInput) {
                dropdown.style.display = 'none';
            }
        });
    }

    // ==================== 批量操作 ====================

    function showBatchActions(count) {
        if (batchActions) {
            batchActions.style.display = 'flex';
            leftPanel.classList.add('has-batch-actions');
        }
        if (batchCount) {
            batchCount.textContent = t('batch.selected', { n: count });
        }
    }

    function hideBatchActions() {
        if (batchActions) {
            batchActions.style.display = 'none';
            leftPanel.classList.remove('has-batch-actions');
        }
    }

    function updateTagSelect() {
        const select = document.getElementById('tagSelect');
        if (!select) return;
        const currentValue = select.value;
        select.innerHTML = '<option value="">' + t('sidebar.select_tag') + '</option>';
        for (const tag of tags) {
            const option = document.createElement('option');
            option.value = tag.id;
            option.textContent = (tag.icon && tag.icon.indexOf('data:') !== 0 ? tag.icon + ' ' : '') + tag.name;
            select.appendChild(option);
        }
        if (currentValue && tags.some(t => t.id === currentValue)) {
            select.value = currentValue;
        }
    }

    // ==================== 公开 API ====================

    // ★ 根据图片路径在导航栏中定位并高亮对应文件夹
    async function navigateToFolder(imagePath, rootPath, folder) {
        // 构建文件夹完整路径
        let targetFolder;
        let isRootFolder = false;
        if (folder) {
            const rootNorm = (rootPath || '').replace(/\\/g, '/');
            const folderNorm = folder.replace(/\\/g, '/');
            targetFolder = rootNorm + '/' + folderNorm;
        } else {
            // 图片在根目录中
            targetFolder = (rootPath || '').replace(/\\/g, '/');
            isRootFolder = true;
        }

        // 切换到文件夹标签
        switchTab('folder');
        await refreshFolderTree();

        // 等待 DOM 渲染
        await new Promise(r => setTimeout(r, 100));

        // 图片在根目录，直接定位根节点
        if (isRootFolder) {
            const rootNorm = targetFolder.toLowerCase();
            const root = folderRoots.find(r => (r.path || '').replace(/\\/g, '/').toLowerCase() === rootNorm);
            if (root) {
                root.expanded = true;
            } else {
                App.showToast(t('toast.picture_in_root'), 'info');
                return;
            }
        }

        // 在 folderRoots 中查找匹配节点并构建需要展开的路径链
        const targetNorm = targetFolder.toLowerCase();
        function findAndExpand(roots, pathChain) {
            for (const root of roots) {
                const rootNorm = (root.path || '').replace(/\\/g, '/').toLowerCase();
                if (targetNorm === rootNorm || targetNorm.startsWith(rootNorm + '/')) {
                    root.expanded = true;
                    if (targetNorm === rootNorm) {
                        return { node: root, chain: [...pathChain, root] };
                    }
                    function searchChildren(nodes, chain) {
                        for (const child of nodes) {
                            const childNorm = (child.path || '').replace(/\\/g, '/').toLowerCase();
                            child.expanded = true;
                            if (targetNorm === childNorm) {
                                return { node: child, chain: [...chain, child] };
                            }
                            if (targetNorm.startsWith(childNorm + '/') && child.children && child.children.length > 0) {
                                const result = searchChildren(child.children, [...chain, child]);
                                if (result) return result;
                            }
                            child.expanded = false;
                        }
                        return null;
                    }
                    const result = searchChildren(root.children || [], [...pathChain, root]);
                    if (result) return result;
                }
            }
            return null;
        }

        const result = findAndExpand(folderRoots, []);
        if (!result) {
            App.showToast(t('toast.folder_not_in_nav'), 'warning');
            return;
        }

        // 重新渲染以应用展开状态
        renderFolderTree();
        await new Promise(r => setTimeout(r, 50));

        // 在 DOM 中定位并高亮对应节点
        const headers = folderTree.querySelectorAll('.tree-node-header');
        let targetHeader = null;
        for (const h of headers) {
            const hPath = (h.dataset.path || '').replace(/\\/g, '/').toLowerCase();
            if (hPath === targetNorm) {
                targetHeader = h;
                break;
            }
        }

        if (targetHeader) {
            // 清除旧高亮
            folderTree.querySelectorAll('.tree-node-header.active').forEach(el => el.classList.remove('active'));
            // 设置新高亮
            targetHeader.classList.add('active');
            activeFolderPath = targetHeader.dataset.path;
            // 闪烁动画
            targetHeader.style.transition = 'background 0.15s';
            targetHeader.style.background = 'var(--accent)';
            targetHeader.style.color = '#fff';
            setTimeout(() => {
                targetHeader.style.background = '';
                targetHeader.style.color = '';
            }, 1500);
            // 滚动到可见
            targetHeader.scrollIntoView({ block: 'nearest', behavior: 'smooth' });
        }
    }

    // 轻量更新文件夹计数（不重建 DOM），用于增量扫描时实时更新数字
    function _updateFolderCounts(rootPath, totalCount) {
        if (!folderTree) return;
        const rootNode = folderTree.querySelector('.tree-node[data-path="' + CSS.escape(rootPath) + '"]');
        if (!rootNode) return;
        const countEl = rootNode.querySelector(':scope > .tree-node-header > .tree-count');
        if (countEl) {
            countEl.textContent = totalCount;
            countEl.dataset.total = totalCount;
        }
    }

    // ★ 轻量更新单个文件夹的计数 + 子节点，不重建整棵树。
    // 从后端 GetFolderCount 获取最新值，然后局部更新 folderRoots 数据 + DOM。
    async function _updateSingleFolderCount(folderPath) {
        if (!folderPath) return;
        const normalizedTarget = folderPath.replace(/\\/g, '/').toLowerCase();

        // 从后端只查这一个文件夹的 count
        let count = -1;
        try {
            if (typeof WailsBridge !== 'undefined' && WailsBridge.isWails()) {
                count = await WailsBridge.getFolderCount(folderPath);
            }
        } catch (e) {
            // 后端不可用，回退到整树刷新
            return refreshFolderTree();
        }
        if (count < 0) return;

        // 更新 folderRoots 中的数据
        function updateNode(nodes) {
            for (const node of nodes) {
                const nodePath = (node.path || '').replace(/\\/g, '/').toLowerCase();
                if (nodePath === normalizedTarget) {
                    node.imageCount = count;
                    return true;
                }
                if (node.children && node.children.length > 0) {
                    if (updateNode(node.children)) return true;
                }
            }
            return false;
        }
        updateNode(folderRoots);

        // 轻量更新 DOM（只更新对应节点的 count 文字，不重建整棵树）
        if (folderTree) {
            const rootNode = folderTree.querySelector('.tree-node[data-path="' + CSS.escape(folderPath) + '"]');
            if (rootNode) {
                const countEl = rootNode.querySelector(':scope > .tree-node-header > .tree-count');
                if (countEl) {
                    countEl.textContent = count;
                    countEl.dataset.total = count;
                }
            }
        }
    }

    return {
        init,
        refreshFolderTree,
        refreshTagTree,
        updateIndexStatusUI,
        refreshApiConfigSelect,
        invalidateFolderTree,
        invalidateTagTree,
        showBatchActions,
        hideBatchActions,
        updateTagSelect,
        switchTab,
        navigateToFolder,
        _updateFolderCounts,
        _updateSingleFolderCount,
        // 供外部调用的编辑模式状态
        isEditingFolderOrder: () => isEditingFolderOrder
    };
})();
