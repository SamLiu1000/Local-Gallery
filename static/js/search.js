/* ============================================================
   search.js - 搜索模块
     提供图片元数据搜索功能：
     - 搜索栏 UI 管理（顶部栏）
     - 基本搜索 + 高级搜索
     - 与 Gallery 模块集成
     ============================================================ */

const SearchModule = (() => {
    const t = (typeof I18n !== "undefined" ? I18n.t : (s) => s);
    // DOM - 基本
    let searchBar;
    let searchInput;
    let searchClearBtn;
    let searchHint;
    let searchActions;
    let searchShowAllBtn;
    let searchLoadMoreBtn;
    let searchBtn;

    // DOM - 高级
    let advancedSearchToggle;
    let advancedSearchPanel;
    let advMatchMode;
    let folderCheckboxList;
    let folderFilterInput;
    let advDateFrom;
    let advDateTo;
    let conditionsList;
    let addConditionBtn;
    let advSearchApply;
    let advSearchReset;
    let advSearchFoldersCheck;
    let conditionRowCount = 1;

    // DOM - 搜索历史
    let searchHistoryPanel;
    let searchHistoryList;
    let searchHistoryClear;

    // DOM - 生成参数标签
    let paramTagsToggle;
    let paramTagsPanel;
    let paramTagsBody;
    let paramTagsFilter;
    let paramTagsRefresh;
    let paramTagsGroups = [];      // 后端返回的类别分组缓存
    let paramTagsRendered = false; // 是否已渲染（避免每次打开重复请求）
    let paramTagsDirty = false;    // ★ 语言切换后类别名需重渲染（切换时面板可能是关着的）
    let paramTagsExpanded = new Set(); // 已展开的类别 key（默认全部收缩，点击表头展开）
    const PARAM_TAGS_EXPANDED_KEY = 'paramTagsExpanded'; // 展开状态持久化（localStorage）

    // 状态
    let debounceTimer = null;
    let isSearchActive = false;
    let currentQuery = '';
    let currentResults = [];
    let currentTotal = 0;
    let currentOffset = 0;
    const PAGE_SIZE = 1000;
    const SCROLL_LOAD_SIZE = 100;  // 滚动触发的增量加载批次大小

    // 高级搜索状态
    let isAdvancedMode = false;
    let selectedFolders = [];
    let currentSearchParams = null;  // 保存当前搜索参数，用于 loadMore
    let isLoadingMore = false;      // 防止滚动时重复触发加载
    let scrollLoadEnabled = false;  // 是否启用滚动自动加载
    // 生成参数标签点击触发的搜索（不在高级面板 DOM 条件里，单独记录用于显示与高亮）
    let currentTagSearch = null;    // { key, value }

    // 搜索历史（持久化到 Storage）
    let searchHistory = [];
    const HISTORY_KEY = 'searchHistory';
    const HISTORY_MAX = 20;

    // 回调
    let onSearchResults = null;
    let onClearSearch = null;

    // ==================== 初始化 ====================

    function init(callbacks) {
        if (callbacks) {
            onSearchResults = callbacks.onSearchResults;
            onClearSearch = callbacks.onClearSearch;
        }

        searchBar = document.getElementById('searchBar');
        searchInput = document.getElementById('searchInput');
        searchClearBtn = document.getElementById('searchClearBtn');
        searchHint = document.getElementById('searchHint');
        searchActions = document.getElementById('searchActions');
        searchShowAllBtn = document.getElementById('searchShowAllBtn');
        searchLoadMoreBtn = document.getElementById('searchLoadMoreBtn');
        searchBtn = document.getElementById('searchBtn');

        bindEvents();
        initAdvancedUI();
        initParamTags();
        initScrollLoad();
        bindSearchHistory();
        bindGlobalShortcuts();
        loadSearchHistory();

        // ★ 修复：语言切换后重新翻译动态提示（"找到 N 结果"等 JS 写入的文本
        //   不参与 data-i18n 静态扫描）和历史列表的空状态文案
        window.addEventListener('i18n:changed', () => {
            renderHint();
            if (searchHistoryPanel && searchHistoryPanel.style.display !== 'none') {
                renderSearchHistory();
            }
        });
    }

    function bindEvents() {
        if (!searchInput) return;

        // 输入事件 - 管理 UI 状态
        searchInput.addEventListener('input', () => {
            const query = searchInput.value.trim();
            if (searchClearBtn) {
                searchClearBtn.style.display = query ? 'flex' : 'none';
            }
            // 用户输入时隐藏搜索结果提示，避免文字重叠
            if (query && isSearchActive) {
                hideHint();
            }
            if (!query && isSearchActive) {
                exitSearch();
            }
        });

        // 聚焦输入框时隐藏提示，避免与光标/输入文字重叠
        searchInput.addEventListener('focus', () => {
            if (isSearchActive) {
                hideHint();
            }
        });

        // Enter 键搜索
        searchInput.addEventListener('keydown', (e) => {
            if (e.key === 'Enter') {
                e.preventDefault();
                performSearch();
            }
            if (e.key === 'Escape') {
                exitSearch();
            }
        });

        // 清除按钮：有搜索则退出搜索，否则直接清空输入框
        if (searchClearBtn) {
            searchClearBtn.addEventListener('click', () => {
                if (isSearchActive) {
                    exitSearch();
                } else {
                    searchInput.value = '';
                    searchClearBtn.style.display = 'none';
                    searchInput.focus();
                }
            });
        }

        // 搜索按钮
        if (searchBtn) {
            searchBtn.addEventListener('click', () => performSearch());
        }

        // 显示全部
        if (searchShowAllBtn) {
            searchShowAllBtn.addEventListener('click', () => showAllResults());
        }

        // 加载更多
        if (searchLoadMoreBtn) {
            searchLoadMoreBtn.addEventListener('click', () => loadMore());
        }

        // 全局 Esc
        document.addEventListener('keydown', (e) => {
            if (e.key === 'Escape' && isSearchActive && document.activeElement !== searchInput) {
                exitSearch();
            }
        });
    }

    // ==================== 全局快捷键 ====================

    // '/' 聚焦搜索框（守卫模式与 gallery/detail 一致：输入框/查看器打开时不抢事件）
    function bindGlobalShortcuts() {
        document.addEventListener('keydown', (e) => {
            if (e.key !== '/') return;
            const el = document.activeElement;
            if (el && (el.tagName === 'INPUT' || el.tagName === 'TEXTAREA' || el.isContentEditable)) return;
            const overlay = document.getElementById('imageViewerOverlay');
            if (overlay && overlay.style.display === 'flex') return;
            e.preventDefault();
            if (searchInput) searchInput.focus();
        });
    }

    // ==================== 搜索历史 ====================

    function bindSearchHistory() {
        searchHistoryPanel = document.getElementById('searchHistoryPanel');
        searchHistoryList = document.getElementById('searchHistoryList');
        searchHistoryClear = document.getElementById('searchHistoryClear');

        if (!searchHistoryPanel || !searchInput) return;

        // 聚焦时展示历史
        searchInput.addEventListener('focus', () => {
            if (searchHistory.length > 0) {
                renderSearchHistory();
                searchHistoryPanel.style.display = 'block';
            }
        });

        // 输入时收起历史，避免与输入内容重叠
        searchInput.addEventListener('input', () => {
            if (searchHistoryPanel) searchHistoryPanel.style.display = 'none';
        });

        // 点击面板内部不冒泡（防止外层 document click 收起）
        searchHistoryPanel.addEventListener('mousedown', (e) => e.preventDefault());

        if (searchHistoryClear) {
            searchHistoryClear.addEventListener('click', (e) => {
                e.stopPropagation();
                searchHistory = [];
                saveSearchHistoryList();
                renderSearchHistory();
                if (searchHistoryPanel) searchHistoryPanel.style.display = 'none';
            });
        }

        // 点击历史条目：回填输入框并执行简单搜索
        if (searchHistoryList) {
            searchHistoryList.addEventListener('click', (e) => {
                const item = e.target.closest('.search-history-item');
                if (!item) return;
                const q = item.dataset.query;
                if (!q) return;
                if (searchInput) searchInput.value = q;
                if (searchHistoryPanel) searchHistoryPanel.style.display = 'none';
                if (searchClearBtn) searchClearBtn.style.display = 'flex';
                // 清掉可能残留的高级搜索状态，按简单搜索执行
                selectedFolders = [];
                performSimpleSearch(q);
            });
        }

        // 点击面板外部收起
        document.addEventListener('click', (e) => {
            if (searchHistoryPanel && searchHistoryPanel.style.display !== 'none' &&
                !searchHistoryPanel.contains(e.target) && e.target !== searchInput) {
                searchHistoryPanel.style.display = 'none';
            }
        });
    }

    async function loadSearchHistory() {
        try {
            if (typeof Storage !== 'undefined' && Storage.getSetting) {
                const saved = await Storage.getSetting(HISTORY_KEY, []);
                if (Array.isArray(saved)) searchHistory = saved;
            }
        } catch (e) { /* 读取失败忽略 */ }
    }

    async function saveSearchHistory(query) {
        if (!query) return;
        query = query.trim();
        if (!query) return;
        // 去重（最近优先），最多保留 HISTORY_MAX 条
        searchHistory = [query, ...searchHistory.filter(q => q !== query)].slice(0, HISTORY_MAX);
        try {
            if (typeof Storage !== 'undefined' && Storage.setSetting) {
                await Storage.setSetting(HISTORY_KEY, searchHistory);
            }
        } catch (e) { /* 保存失败忽略 */ }
    }

    function renderSearchHistory() {
        if (!searchHistoryList) return;
        searchHistoryList.innerHTML = '';
        if (searchHistory.length === 0) {
            const empty = document.createElement('div');
            empty.className = 'search-history-empty';
            empty.textContent = t('search.history_empty');
            searchHistoryList.appendChild(empty);
            return;
        }
        for (const q of searchHistory) {
            const item = document.createElement('div');
            item.className = 'search-history-item';
            item.dataset.query = q;
            item.title = q;
            const text = document.createElement('span');
            text.className = 'search-history-text';
            text.textContent = q;
            item.appendChild(text);
            searchHistoryList.appendChild(item);
        }
    }

    // ==================== 高级搜索 UI 初始化 ====================

    function initAdvancedUI() {
        advancedSearchToggle = document.getElementById('advancedSearchToggle');
        advancedSearchPanel = document.getElementById('advancedSearchPanel');
        advMatchMode = document.getElementById('advMatchMode');
        folderCheckboxList = document.getElementById('folderCheckboxList');
        folderFilterInput = document.getElementById('folderFilterInput');
        advDateFrom = document.getElementById('advDateFrom');
        advDateTo = document.getElementById('advDateTo');
        conditionsList = document.getElementById('conditionsList');
        addConditionBtn = document.getElementById('addConditionBtn');
        advSearchApply = document.getElementById('advSearchApply');
        advSearchReset = document.getElementById('advSearchReset');
        advSearchFoldersCheck = document.getElementById('advSearchFoldersCheck');

        if (!advancedSearchToggle) return;

        // 日期输入：点击时弹出原生日期选择器，内部存储 yyyy-mm-dd，显示为 yyyy/mm/dd
        function setupDateInput(textInput) {
            const hiddenInput = document.createElement('input');
            hiddenInput.type = 'date';
            hiddenInput.style.cssText = 'position:absolute;opacity:0;pointer-events:none;width:0;height:0;';
            textInput.parentNode.insertBefore(hiddenInput, textInput);

            textInput.addEventListener('click', () => {
                if (textInput.value) {
                    hiddenInput.value = textInput.value.replace(/\//g, '-');
                }
                if (typeof hiddenInput.showPicker === 'function') {
                    hiddenInput.showPicker();
                } else {
                    hiddenInput.focus();
                }
            });

            hiddenInput.addEventListener('change', () => {
                if (hiddenInput.value) {
                    const parts = hiddenInput.value.split('-');
                    textInput.value = parts[0] + '/' + parts[1] + '/' + parts[2];
                }
            });

            // 允许手动输入 yyyy/mm/dd 格式
            textInput.addEventListener('input', () => {
                textInput.dataset.raw = textInput.value.replace(/\//g, '-');
            });
        }
        setupDateInput(advDateFrom);
        setupDateInput(advDateTo);

        // 切换高级面板
        advancedSearchToggle.addEventListener('click', (e) => {
            console.log('[Search] advancedSearchToggle clicked, target:', e.target.tagName);
            const visible = advancedSearchPanel.style.display !== 'none';
            advancedSearchPanel.style.display = visible ? 'none' : 'flex';
            advancedSearchToggle.classList.toggle('active', !visible);
            // 打开高级面板时收起生成参数标签面板
            if (!visible && paramTagsPanel) {
                paramTagsPanel.style.display = 'none';
                if (paramTagsToggle) paramTagsToggle.classList.remove('active');
            }
            if (!visible) {
                populateFolderCheckboxes();
            }
        });

        // 文件夹筛选输入框：实时过滤列表
        if (folderFilterInput) {
            folderFilterInput.addEventListener('input', () => {
                filterFolderCheckboxes();
            });
        }

        // 添加条件
        addConditionBtn.addEventListener('click', () => {
            conditionRowCount++;
            const row = document.createElement('div');
            row.className = 'condition-row';
            row.id = 'conditionRow' + conditionRowCount;
            row.innerHTML = `
                <select class="condition-field">
                    <option value="all" data-i18n="search.all_fields">全部字段</option>
                    <option value="path" data-i18n="search.field.path">路径</option>
                    <option value="prompt" data-i18n="search.field.prompt">提示词</option>
                    <option value="negative_prompt" data-i18n="search.field.negative_prompt">反向提示词</option>
                    <option value="params_json" data-i18n="search.field.params">参数</option>
                    <option value="folder_name" data-i18n="search.field.folder_name">文件夹名称</option>
                    <option value="param_sampler" data-i18n="search.field.param_sampler">采样器</option>
                    <option value="param_model" data-i18n="search.field.param_model">模型</option>
                    <option value="param_steps" data-i18n="search.field.param_steps">步数</option>
                    <option value="param_seed" data-i18n="search.field.param_seed">种子</option>
                    <option value="param_cfg" data-i18n="search.field.param_cfg">CFG</option>
                    <option value="param_size" data-i18n="search.field.param_size">尺寸</option>
                    <option value="param_vae" data-i18n="search.field.param_vae">VAE</option>
                    <option value="param_lora" data-i18n="search.field.param_lora">LoRA</option>
                </select>
                <select class="condition-mode">
                    <option value="contains" data-i18n="search.mode.contains">包含</option>
                    <option value="exact" data-i18n="search.mode.exact">精确匹配</option>
                    <option value="exclude" data-i18n="search.mode.exclude">排除</option>
                    <option value="word" data-i18n="search.mode.word">全词匹配</option>
                </select>
                <input type="text" class="condition-value" data-i18n-placeholder="search.keyword_placeholder" placeholder="输入关键词..." />
                <button class="condition-remove-btn btn-small"><span class="icon icon-close"></span></button>
            `;
            row.querySelector('.condition-remove-btn').addEventListener('click', (e) => {
                e.stopPropagation();
                row.remove();
            });
            conditionsList.appendChild(row);
            // ★ 动态插入后应用翻译（否则 data-i18n 选项会显示原始 key）
            if (typeof I18n !== 'undefined' && I18n.scanDOM) I18n.scanDOM();
        });

        // 应用按钮
        advSearchApply.addEventListener('click', () => {
            const hasConditions = collectAdvancedConditions().length > 0;
            const hasFolders = selectedFolders.length > 0;
            const hasDateRange = (advDateFrom && advDateFrom.value) || (advDateTo && advDateTo.value);
            const hasFolderSearch = advSearchFoldersCheck && advSearchFoldersCheck.checked && searchInput && searchInput.value.trim();
            if (!hasConditions && !hasFolders && !hasDateRange && !hasFolderSearch) {
                if (typeof App !== 'undefined' && App.showToast) {
                    App.showToast(t('search.need_condition'), 'warning');
                }
                return;
            }
            advancedSearchPanel.style.display = 'none';
            advancedSearchToggle.classList.remove('active');
            performSearch();
        });

        // 重置按钮
        advSearchReset.addEventListener('click', () => {
            selectedFolders = [];
            isAdvancedMode = false;
            currentSearchParams = null;
            currentTagSearch = null;
            if (folderCheckboxList) populateFolderCheckboxes();
            if (folderFilterInput) folderFilterInput.value = '';
            if (advDateFrom) advDateFrom.value = '';
            if (advDateTo) advDateTo.value = '';
            if (advSearchFoldersCheck) advSearchFoldersCheck.checked = false;
            // 移除所有额外条件行，只保留第一个
            conditionRowCount = 1;
            if (conditionsList) {
                conditionsList.innerHTML = `
                    <div class="condition-row" id="conditionRow0">
                        <select class="condition-field">
                            <option value="all" data-i18n="search.all_fields">全部字段</option>
                            <option value="path" data-i18n="search.field.path">路径</option>
                            <option value="prompt" data-i18n="search.field.prompt">提示词</option>
                            <option value="negative_prompt" data-i18n="search.field.negative_prompt">反向提示词</option>
                            <option value="params_json" data-i18n="search.field.params">参数</option>
                            <option value="folder_name" data-i18n="search.field.folder_name">文件夹名称</option>
                            <option value="param_sampler" data-i18n="search.field.param_sampler">采样器</option>
                            <option value="param_model" data-i18n="search.field.param_model">模型</option>
                            <option value="param_steps" data-i18n="search.field.param_steps">步数</option>
                            <option value="param_seed" data-i18n="search.field.param_seed">种子</option>
                            <option value="param_cfg" data-i18n="search.field.param_cfg">CFG</option>
                            <option value="param_size" data-i18n="search.field.param_size">尺寸</option>
                            <option value="param_vae" data-i18n="search.field.param_vae">VAE</option>
                            <option value="param_lora" data-i18n="search.field.param_lora">LoRA</option>
                        </select>
                        <select class="condition-mode">
                            <option value="contains" data-i18n="search.mode.contains">包含</option>
                            <option value="exact" data-i18n="search.mode.exact">精确匹配</option>
                            <option value="exclude" data-i18n="search.mode.exclude">排除</option>
                            <option value="word" data-i18n="search.mode.word">全词匹配</option>
                        </select>
                        <input type="text" class="condition-value" data-i18n-placeholder="search.keyword_placeholder" placeholder="输入关键词..." />
                        <button class="condition-remove-btn btn-small" style="display:none;"><span class="icon icon-close"></span></button>
                    </div>
                `;
                if (typeof I18n !== 'undefined' && I18n.scanDOM) I18n.scanDOM();
            }
            if (advMatchMode) advMatchMode.value = 'or';
            if (searchInput) searchInput.value = '';
            if (typeof App !== 'undefined' && App.showToast) {
                App.showToast(t('search.advanced_reset'), 'info');
            }
        });

        // 点击面板外部关闭
        document.addEventListener('click', (e) => {
            if (advancedSearchPanel && advancedSearchPanel.style.display !== 'none') {
                if (!advancedSearchPanel.contains(e.target) && e.target !== advancedSearchToggle) {
                    advancedSearchPanel.style.display = 'none';
                    advancedSearchToggle.classList.remove('active');
                }
            }
        });
    }

    // ==================== 生成参数标签面板 ====================

    // 类别显示名：优先用 i18n key 映射，取不到就回退到原始参数键
    const PARAM_CAT_LABEL_KEYS = {
        'Model': 'search.field.param_model',
        'LoRA': 'search.field.param_lora',
        'Sampler': 'search.field.param_sampler',
        'Scheduler': 'search.paramtag.scheduler',
        'Schedule type': 'search.paramtag.schedule_type',
        'CFG Scale': 'search.field.param_cfg',
        'Distilled CFG Scale': 'search.paramtag.distilled_cfg',
        'Steps': 'search.field.param_steps',
        'Size': 'search.field.param_size',
        'VAE': 'search.field.param_vae',
        'CLIP': 'search.paramtag.clip',
        'Clip Skip': 'search.paramtag.clip_skip',
        'Denoising strength': 'search.paramtag.denoising',
        'Hires upscaler': 'search.paramtag.hires_upscaler',
        'Hires upscale': 'search.paramtag.hires_upscale',
        'Hires steps': 'search.paramtag.hires_steps',
    };

    function paramCatLabel(key) {
        const k = PARAM_CAT_LABEL_KEYS[key];
        if (k) {
            const label = t(k);
            if (label !== k) return label;
        }
        return key;
    }

    function initParamTags() {
        paramTagsToggle = document.getElementById('paramTagsToggle');
        paramTagsPanel = document.getElementById('paramTagsPanel');
        paramTagsBody = document.getElementById('paramTagsBody');
        paramTagsFilter = document.getElementById('paramTagsFilter');
        paramTagsRefresh = document.getElementById('paramTagsRefresh');

        // 恢复持久化的展开状态
        try {
            const saved = localStorage.getItem(PARAM_TAGS_EXPANDED_KEY);
            if (saved) {
                const arr = JSON.parse(saved);
                if (Array.isArray(arr)) paramTagsExpanded = new Set(arr);
            }
        } catch (e) { /* 解析失败使用默认全收缩 */ }

        if (!paramTagsToggle || !paramTagsPanel) return;

        // 切换面板
        paramTagsToggle.addEventListener('click', (e) => {
            e.stopPropagation();
            const visible = paramTagsPanel.style.display !== 'none';
            if (visible) {
                closeParamTags();
                return;
            }
            paramTagsPanel.style.display = 'flex';
            paramTagsToggle.classList.add('active');
            // 打开生成参数面板时收起高级搜索面板
            if (advancedSearchPanel && advancedSearchPanel.style.display !== 'none') {
                advancedSearchPanel.style.display = 'none';
                if (advancedSearchToggle) advancedSearchToggle.classList.remove('active');
            }
            if (paramTagsRendered && paramTagsDirty) {
                // ★ 语言切换后重渲染：类别名是 i18n 翻译的（chips 是数据，不需要翻译）
                paramTagsDirty = false;
                renderParamTags();
            } else if (paramTagsRendered) {
                applyParamTagFilter(); // 重新应用筛选
            } else {
                loadParamTags(false);
            }
        });

        // 筛选输入
        if (paramTagsFilter) {
            paramTagsFilter.addEventListener('input', () => applyParamTagFilter());
        }

        // 刷新按钮：强制重新聚合
        if (paramTagsRefresh) {
            paramTagsRefresh.addEventListener('click', (e) => {
                e.stopPropagation();
                loadParamTags(true);
            });
        }

        // 点击面板外部关闭
        document.addEventListener('click', (e) => {
            if (paramTagsPanel && paramTagsPanel.style.display !== 'none') {
                if (!paramTagsPanel.contains(e.target) && e.target !== paramTagsToggle) {
                    closeParamTags();
                }
            }
        });
        // 语言切换后重渲染（类别名是 i18n 翻译的）
        window.addEventListener('i18n:changed', () => {
            if (!paramTagsRendered) return;
            if (paramTagsPanel && paramTagsPanel.style.display !== 'none') {
                renderParamTags();     // 面板开着：立即重渲染
                paramTagsDirty = false;
            } else {
                // ★ 面板关着：标记为脏，下次打开时重渲染。
                //   原来只在"面板开着"时重渲染，于是切语言后打开面板，类别名仍是旧语言。
                paramTagsDirty = true;
            }
        });
    }

    function closeParamTags() {
        if (paramTagsPanel) paramTagsPanel.style.display = 'none';
        if (paramTagsToggle) paramTagsToggle.classList.remove('active');
    }

    /** 拉取生成参数标签分组（force=true 时绕过缓存强制重新聚合） */
    async function loadParamTags(force) {
        if (!paramTagsBody) return;
        if (!paramTagsRendered) {
            paramTagsBody.innerHTML = '<p class="param-tags-empty">' + t('search.param_tags_loading') + '</p>';
        }
        try {
            const response = await WailsBridge.getParamTags(!!force);
            if (!response || !response.success) {
                paramTagsBody.innerHTML = '<p class="param-tags-empty">' + (response?.message || t('search.param_tags_load_failed')) + '</p>';
                return;
            }
            paramTagsGroups = response.groups || [];
            paramTagsRendered = true;
            renderParamTags();
        } catch (err) {
            console.error('[Search] 加载生成参数标签失败:', err);
            paramTagsBody.innerHTML = '<p class="param-tags-empty">' + t('search.param_tags_load_failed') + '</p>';
        }
    }

    /** 渲染所有类别分组（受当前筛选词约束） */
    function renderParamTags() {
        if (!paramTagsBody) return;
        paramTagsBody.innerHTML = '';

        if (!paramTagsGroups || paramTagsGroups.length === 0) {
            paramTagsBody.innerHTML = '<p class="param-tags-empty">' + t('search.param_tags_empty') + '</p>';
            return;
        }

        // 逐个类别渲染：默认收缩，折叠的类别只渲染表头（打开面板瞬间完成）。
        // 展开时才惰性生成标签 chip（buildParamTagChips），保证大型列表（如 LoRA 上万个标签）也流畅
        const VISIBLE = 150;
        const frag = document.createDocumentFragment();
        for (const group of paramTagsGroups) {
            if (!group || !group.tags || group.tags.length === 0) continue;
            const section = document.createElement('div');
            section.className = 'param-tag-group';
            section.dataset.key = group.key;
            // ★ 默认收缩：仅当类别 key 在已展开集合中才显示标签
            const isExpanded = paramTagsExpanded.has(group.key);
            section.classList.toggle('collapsed', !isExpanded);

            // ★ 类别表头可点击：展开/收缩整个类别
            const header = document.createElement('button');
            header.type = 'button';
            header.className = 'param-tag-group-header';
            header.addEventListener('click', () => {
                toggleParamTagGroup(group.key);
            });

            const chevron = document.createElement('span');
            chevron.className = 'param-tag-chevron';
            chevron.textContent = '▸';

            const nameSpan = document.createElement('span');
            nameSpan.className = 'param-tag-group-name';
            nameSpan.textContent = paramCatLabel(group.key);
            const countSpan = document.createElement('span');
            countSpan.className = 'param-tag-group-count';
            countSpan.textContent = group.tags.length;
            header.appendChild(chevron);
            header.appendChild(nameSpan);
            header.appendChild(countSpan);
            section.appendChild(header);

            // 展开的类别立即生成标签；收缩的延迟到展开时生成（见 toggleParamTagGroup）
            if (isExpanded) {
                buildParamTagChips(section, group, VISIBLE);
            }

            frag.appendChild(section);
        }
        paramTagsBody.appendChild(frag);
        applyParamTagFilter();
    }

    /**
     * 为某个类别生成标签 chip 区（含"显示更多"）。
     * section 需已有 .param-tag-group-header；此函数会追加 .param-tag-chips 与"显示更多"按钮。
     */
    function buildParamTagChips(section, group, VISIBLE) {
        if (section.querySelector('.param-tag-chips')) return; // 已生成过

        const chips = document.createElement('div');
        chips.className = 'param-tag-chips';

        const max = Math.min(group.tags.length, VISIBLE);
        for (let i = 0; i < max; i++) {
            const tag = group.tags[i];
            if (!tag) continue;
            const chip = document.createElement('button');
            chip.type = 'button';
            chip.className = 'param-tag-chip';
            chip.dataset.value = tag.value;
            chip.dataset.count = tag.count;

            const valueSpan = document.createElement('span');
            valueSpan.className = 'param-tag-chip-value';
            valueSpan.textContent = tag.value;
            valueSpan.title = tag.value;
            chip.appendChild(valueSpan);

            const countEl = document.createElement('span');
            countEl.className = 'param-tag-chip-count';
            countEl.textContent = tag.count;
            chip.appendChild(countEl);

            chip.addEventListener('click', () => {
                onParamTagClick(group, tag);
            });
            chips.appendChild(chip);
        }
        section.appendChild(chips);

        // 折叠剩余标签：追加"显示更多"按钮
        if (group.tags.length > VISIBLE) {
            const more = document.createElement('button');
            more.type = 'button';
            more.className = 'param-tag-more btn-small';
            more.textContent = t('search.param_tags_more', { n: group.tags.length - VISIBLE });
            more.addEventListener('click', (e) => {
                // ★ 阻止冒泡：按钮在展开过程中会被移除，若 click 冒泡到 document，
                //   面板外点击监听会把整个面板关闭。
                e.stopPropagation();
                // 一次性展开剩余标签（用 fragment 追加，避免重复插入）
                const rest = document.createDocumentFragment();
                for (let i = VISIBLE; i < group.tags.length; i++) {
                    const tag = group.tags[i];
                    if (!tag) continue;
                    const chip = document.createElement('button');
                    chip.type = 'button';
                    chip.className = 'param-tag-chip';
                    chip.dataset.value = tag.value;
                    chip.dataset.count = tag.count;
                    const valueSpan = document.createElement('span');
                    valueSpan.className = 'param-tag-chip-value';
                    valueSpan.textContent = tag.value;
                    valueSpan.title = tag.value;
                    chip.appendChild(valueSpan);
                    const countEl = document.createElement('span');
                    countEl.className = 'param-tag-chip-count';
                    countEl.textContent = tag.count;
                    chip.appendChild(countEl);
                    chip.addEventListener('click', () => {
                        onParamTagClick(group, tag);
                    });
                    rest.appendChild(chip);
                }
                chips.appendChild(rest);
                // ★ 延迟移除按钮：本次 click 事件分发完成后再从 DOM 移除，
                //   避免移除瞬间下方新增标签占据原位置而误收本次点击（点击穿透）。
                more.style.display = 'none';
                setTimeout(() => more.remove(), 0);
            });
            section.appendChild(more);
        }
    }

    /** 展开/收缩某个类别（点击表头），并持久化状态 */
    function toggleParamTagGroup(key) {
        const wasExpanded = paramTagsExpanded.has(key);
        if (wasExpanded) {
            paramTagsExpanded.delete(key);
        } else {
            paramTagsExpanded.add(key);
        }
        // 持久化展开状态
        try {
            localStorage.setItem(PARAM_TAGS_EXPANDED_KEY, JSON.stringify([...paramTagsExpanded]));
        } catch (e) { /* 忽略 */ }
        const section = paramTagsBody.querySelector('.param-tag-group[data-key="' + CSS.escape(key) + '"]');
        if (section) {
            section.classList.toggle('collapsed', !paramTagsExpanded.has(key));
            // ★ 惰性生成标签：首次展开时才创建 chip（折叠时只渲染了表头）
            if (paramTagsExpanded.has(key) && !section.querySelector('.param-tag-chips')) {
                const group = paramTagsGroups.find(g => g && g.key === key);
                if (group) {
                    buildParamTagChips(section, group, 150);
                    applyParamTagFilter();
                }
            }
        }
    }

    /** 按筛选词过滤标签与类别（实时）。筛选时自动展开有匹配的类别，否则保持收缩状态 */
    function applyParamTagFilter() {
        if (!paramTagsFilter || !paramTagsBody) return;
        const q = paramTagsFilter.value.trim().toLowerCase();
        const groups = paramTagsBody.querySelectorAll('.param-tag-group');
        for (const group of groups) {
            // ★ 有筛选词且该类别还未生成 chip 时，惰性生成（收缩状态下只渲染了表头）
            if (q && !group.querySelector('.param-tag-chips')) {
                const key = group.dataset.key;
                const g = paramTagsGroups.find(x => x && x.key === key);
                if (g) buildParamTagChips(group, g, 150);
            }

            if (q) {
                // 有筛选词：按 chip 命中隐藏无匹配的组
                let groupVisible = false;
                const chips = group.querySelectorAll('.param-tag-chip');
                for (const chip of chips) {
                    const val = (chip.dataset.value || '').toLowerCase();
                    const match = val.includes(q);
                    chip.classList.toggle('hidden', !match);
                    if (match) groupVisible = true;
                }
                group.classList.toggle('hidden', !groupVisible);
                // 筛选时展开命中组（否则标签被收缩藏住看不到命中项）
                group.classList.remove('collapsed');
            } else {
                // 无筛选词：全部组可见，恢复用户的展开/收缩状态
                group.classList.remove('hidden');
                const key = group.dataset.key;
                group.classList.toggle('collapsed', key ? !paramTagsExpanded.has(key) : false);
            }
        }
    }

    /** 点击标签：按该参数类别 + 值执行高级搜索 */
    async function onParamTagClick(group, tag) {
        closeParamTags();
        if (paramTagsFilter) paramTagsFilter.value = '';
        // 构造单个条件的高级搜索：field 直接是 "json:Key"（后端 getFields 直传识别）。
        // ★ 标签计数是全局的（全库聚合），搜索范围也固定为全库，保证结果数与标签徽标一致。
        const params = {
            conditions: [{ field: group.field || ('json:' + group.key), mode: group.mode || 'contains', value: tag.value }],
            folders: [],
            dateFrom: 0,
            dateTo: 0,
            matchMode: 'or',
            offset: 0,
            limit: PAGE_SIZE
        };
        await performAdvancedSearch(0, params);
    }

    // ==================== 文件夹复选框列表 ====================

    function getFolderRoots() {
        let roots = [];
        if (typeof Gallery !== 'undefined' && Gallery.getImportedRoots) {
            roots = Gallery.getImportedRoots() || [];
        }
        return roots;
    }

    async function populateFolderCheckboxes() {
        if (!folderCheckboxList) return;

        let roots = getFolderRoots();
        folderCheckboxList.innerHTML = '';

        // ★ 用后端 GetFolders 的准确图片数量覆盖前端估算值（前端 images 数组可能只加载了部分）
        if (typeof WailsBridge !== 'undefined' && WailsBridge.isWails()) {
            try {
                const serverFolders = await WailsBridge.getFolders();
                if (Array.isArray(serverFolders)) {
                    const countMap = new Map();
                    for (const f of serverFolders) {
                        countMap.set((f.path || '').replace(/\\/g, '/'), f.imageCount || 0);
                    }
                    for (const root of roots) {
                        const key = (root.rootId || '').replace(/\\/g, '/');
                        if (countMap.has(key)) {
                            root.imageCount = countMap.get(key);
                        }
                    }
                }
            } catch (e) { /* 后端不可用时保留前端估算值 */ }
        }

        if (roots.length === 0) {
            folderCheckboxList.innerHTML = '<div class="folder-dropdown-empty">' + t('search.no_matching_folders') + '</div>';
            return;
        }

        // ★ 与左侧导航栏排序一致：读取 sidebar_folder_sort（名称/日期/自定义），
        //   再结合 sidebar_folder_order 自定义顺序。
        roots = await sortRootsBySidebarOrder(roots);

        for (const root of roots) {
            const displayName = root.displayName || root.name;
            const label = document.createElement('label');
            label.className = 'folder-checkbox-item';

            const checkbox = document.createElement('input');
            checkbox.type = 'checkbox';
            checkbox.value = root.rootId;
            checkbox.checked = selectedFolders.includes(root.rootId);
            checkbox.addEventListener('change', () => {
                if (checkbox.checked) {
                    if (!selectedFolders.includes(root.rootId)) {
                        selectedFolders.push(root.rootId);
                    }
                } else {
                    selectedFolders = selectedFolders.filter(id => id !== root.rootId);
                }
            });

            const span = document.createElement('span');
            span.textContent = displayName;

            label.appendChild(checkbox);
            label.appendChild(span);

            // ★ 文件夹名称后显示图片数量
            if (root.imageCount != null) {
                const countEl = document.createElement('span');
                countEl.className = 'folder-checkbox-count';
                countEl.textContent = root.imageCount;
                label.appendChild(countEl);
            }

            folderCheckboxList.appendChild(label);
        }
    }

    /**
     * ★ 与左侧导航栏排序一致：
     *   先读取 sidebar_folder_sort（name/date/custom + desc），
     *   若为 name 或 date 则按列排序；否则按 sidebar_folder_order 自定义顺序。
     */
    async function sortRootsBySidebarOrder(roots) {
        try {
            // 1. 读取导航栏列排序设置（与 sidebar.js 一致）
            let sortKey = 'custom';
            let sortDesc = false;
            if (typeof WailsBridge !== 'undefined' && WailsBridge.isWails()) {
                const r = await WailsBridge.getSidebarSetting('sidebar_folder_sort');
                if (r && r.success && r.value) {
                    const parsed = JSON.parse(r.value);
                    if (parsed && (parsed.key === 'name' || parsed.key === 'date' || parsed.key === 'custom')) {
                        sortKey = parsed.key;
                        sortDesc = !!parsed.desc;
                    }
                }
            } else if (typeof Storage !== 'undefined' && Storage.getSetting) {
                const saved = await Storage.getSetting('sidebar_folder_sort', null);
                if (saved && (saved.key === 'name' || saved.key === 'date' || saved.key === 'custom')) {
                    sortKey = saved.key;
                    sortDesc = !!saved.desc;
                }
            }

            // 2. 名称/日期列排序
            if (sortKey === 'name' || sortKey === 'date') {
                const desc = sortDesc;
                return [...roots].sort((a, b) => {
                    if (sortKey === 'name') {
                        const an = (a.displayName || a.name || '').toLowerCase();
                        const bn = (b.displayName || b.name || '').toLowerCase();
                        return desc ? bn.localeCompare(an, 'zh-CN') : an.localeCompare(bn, 'zh-CN');
                    }
                    const ad = a.addedAt || '';
                    const bd = b.addedAt || '';
                    return desc ? bd.localeCompare(ad) : ad.localeCompare(bd);
                });
            }

            // 3. 自定义顺序（sidebar_folder_order）
            let orderList = [];
            if (typeof WailsBridge !== 'undefined' && WailsBridge.isWails()) {
                const result = await WailsBridge.getSidebarSetting('sidebar_folder_order');
                if (result && result.success && result.value) {
                    orderList = JSON.parse(result.value);
                }
            }
            if (!Array.isArray(orderList) && typeof Storage !== 'undefined' && Storage.getSetting) {
                orderList = await Storage.getSetting('sidebar_folder_order', []);
            }
            if (!Array.isArray(orderList) || orderList.length === 0) return roots;

            const orderMap = new Map();
            orderList.forEach((path, index) => orderMap.set(path, index));

            return [...roots].sort((a, b) => {
                const ai = orderMap.has(a.rootId) ? orderMap.get(a.rootId) : Infinity;
                const bi = orderMap.has(b.rootId) ? orderMap.get(b.rootId) : Infinity;
                return ai - bi;
            });
        } catch (e) {
            return roots;
        }
    }

    function filterFolderCheckboxes() {
        if (!folderCheckboxList || !folderFilterInput) return;
        const query = folderFilterInput.value.trim().toLowerCase();
        const items = folderCheckboxList.querySelectorAll('.folder-checkbox-item');
        let hasVisible = false;
        for (const item of items) {
            const text = item.textContent.toLowerCase();
            const match = !query || text.includes(query);
            item.style.display = match ? '' : 'none';
            if (match) hasVisible = true;
        }
        // 无匹配项时显示提示
        let empty = folderCheckboxList.querySelector('.folder-dropdown-empty');
        if (!hasVisible && items.length > 0) {
            if (!empty) {
                empty = document.createElement('div');
                empty.className = 'folder-dropdown-empty';
                empty.textContent = t('search.no_folder_match');
                folderCheckboxList.appendChild(empty);
            }
        } else if (empty) {
            empty.remove();
        }
    }

    // ==================== 收集高级搜索条件 ====================

    function collectAdvancedConditions() {
        const conditions = [];
        if (!conditionsList) return conditions;

        const rows = conditionsList.querySelectorAll('.condition-row');
        for (const row of rows) {
            const fieldSel = row.querySelector('.condition-field');
            const modeSel = row.querySelector('.condition-mode');
            const valueInput = row.querySelector('.condition-value');
            if (!fieldSel || !modeSel || !valueInput) continue;

            const value = valueInput.value.trim();
            if (!value) continue;

            conditions.push({
                field: fieldSel.value,
                mode: modeSel.value,
                value: value
            });
        }
        return conditions;
    }

    function buildAdvancedSearchParams(offset, limit) {
        const conditions = collectAdvancedConditions();
        const isoFrom = advDateFrom && advDateFrom.value ? advDateFrom.value.replace(/\//g, '-') : '';
        const isoTo = advDateTo && advDateTo.value ? advDateTo.value.replace(/\//g, '-') : '';
        const dateFromVal = isoFrom ? new Date(isoFrom).getTime() : 0;
        const dateToVal = isoTo ? new Date(isoTo + 'T23:59:59').getTime() : 0;

        // 如果高级面板没有条件，但搜索输入框有文字，将输入框文字作为条件
        if (conditions.length === 0 && searchInput) {
            const query = searchInput.value.trim();
            if (query) {
                conditions.push({ field: 'all', mode: 'contains', value: query });
            }
        }

        // 如果勾选了"搜索文件夹名称"，追加 folder_name 条件
        // 优先使用搜索栏文字，否则从已有条件中提取值
        if (advSearchFoldersCheck && advSearchFoldersCheck.checked) {
            const searchQuery = searchInput ? searchInput.value.trim() : '';
            if (searchQuery) {
                conditions.push({ field: 'folder_name', mode: 'contains', value: searchQuery });
                console.log('[Search] 文件夹搜索已启用，从搜索栏追加 folder_name:', searchQuery);
            } else {
                // 从条件行的值中提取关键词，为每个非空值创建对应的 folder_name 条件
                for (const c of conditions) {
                    if (c.value && c.field !== 'folder_name') {
                        conditions.push({ field: 'folder_name', mode: 'contains', value: c.value });
                        console.log('[Search] 文件夹搜索已启用，从条件追加 folder_name:', c.value);
                    }
                }
            }
        }

        console.log('[Search] buildAdvancedSearchParams:', JSON.stringify({
            conditions, folders: selectedFolders,
            dateFrom: dateFromVal, dateTo: dateToVal,
            matchMode: advMatchMode ? advMatchMode.value : 'or',
            offset, limit
        }));
        return {
            conditions: conditions,
            folders: selectedFolders,
            dateFrom: dateFromVal,
            dateTo: dateToVal,
            matchMode: advMatchMode ? advMatchMode.value : 'or',
            offset: offset,
            limit: limit
        };
    }

    // ==================== 搜索执行 ====================

    async function performSearch() {
        // 收集高级面板中的条件
        const conditions = collectAdvancedConditions();
        const hasFolders = selectedFolders.length > 0;
        const hasDateRange = (advDateFrom && advDateFrom.value) || (advDateTo && advDateTo.value);
        const searchQuery = searchInput ? searchInput.value.trim() : '';
        // 是否使用高级搜索：高级面板有条件 OR 有文件夹过滤 OR 有日期范围 OR 勾选文件夹搜索
        const hasFolderSearchCheck = advSearchFoldersCheck && advSearchFoldersCheck.checked && (searchQuery || conditions.length > 0);
        console.log('[Search] performSearch:', { conditions: conditions.length, hasFolders, hasDateRange, searchQuery, hasFolderSearchCheck, useAdvanced: conditions.length > 0 || hasFolders || hasDateRange || hasFolderSearchCheck });
        const useAdvanced = conditions.length > 0 || hasFolders || hasDateRange || hasFolderSearchCheck;

        if (!useAdvanced) {
            // 简单搜索
            if (!searchQuery) return;
            // 清除可能残留的高级搜索状态
            selectedFolders = [];
            isAdvancedMode = false;
            currentSearchParams = null;
            currentTagSearch = null;
            await performSimpleSearch(searchQuery);
        } else {
            // 高级搜索：条件来自面板 + 输入框文字，由 buildAdvancedSearchParams 统一处理
            if (conditions.length === 0 && !searchQuery && !hasFolders && !hasDateRange) return;
            await performAdvancedSearch(0);
        }
    }

    async function performSimpleSearch(query) {
        currentQuery = query;
        currentOffset = 0;
        isSearchActive = true;
        isAdvancedMode = false;
        currentSearchParams = null;
        currentTagSearch = null;

        setSearching(true);
        updateHint('search.searching');

        try {
            const response = await WailsBridge.searchImages(query, '', 0, PAGE_SIZE);
            if (!response || !response.success) {
                showSearchError(response?.message || t('search.search_failed'), response?.code);
                return;
            }

            saveSearchHistory(query);

            currentResults = response.items || [];
            currentTotal = response.total || 0;
            updateResultHint(query);
            if (searchInput) searchInput.placeholder = '';

            updateSearchActionsVisibility();

            const galleryImages = convertResultsToGallery(currentResults);
            if (onSearchResults) {
                onSearchResults(galleryImages, query, currentTotal);
            }
            scrollLoadEnabled = currentResults.length < currentTotal;
        } catch (err) {
            console.error('[Search] 搜索失败:', err);
            showSearchError(err.message || t('search.search_request_failed'));
        } finally {
            setSearching(false);
        }
    }

    async function performAdvancedSearch(offset, explicitParams) {
        // 传入 explicitParams（生成参数标签点击）时直接用，否则从高级面板 DOM 收集
        const params = explicitParams || buildAdvancedSearchParams(offset, PAGE_SIZE);
        currentSearchParams = params;
        currentOffset = offset;
        isSearchActive = true;
        isAdvancedMode = true;
        if (explicitParams) {
            // 记录标签搜索来源，供显示与高亮（高级面板条件行里并没有这个条件）
            currentTagSearch = (explicitParams.conditions && explicitParams.conditions[0])
                ? { key: explicitParams.conditions[0].field, value: explicitParams.conditions[0].value }
                : null;
        } else {
            currentTagSearch = null;
        }

        setSearching(true);
        updateHint('search.advanced_searching');

        try {
            console.log('[Search] 调用 WailsBridge.advancedSearch, params:', JSON.stringify(params));
            const response = await WailsBridge.advancedSearch(params);
            console.log('[Search] advancedSearch 返回:', JSON.stringify({ success: response?.success, total: response?.total, message: response?.message, itemsCount: response?.items?.length }));

            if (!response || !response.success) {
                showSearchError(response?.message || t('search.search_failed'), response?.code);
                return;
            }

            if (offset === 0) {
                currentResults = response.items || [];
                // 保存历史：条件值用空格拼接（避免把日期/文件夹等展示文本也存进去，点击重跑更准确）
                const histQuery = params.conditions && params.conditions.length > 0
                    ? params.conditions.map(c => c.value).filter(Boolean).join(' ')
                    : (searchInput ? searchInput.value.trim() : '');
                saveSearchHistory(histQuery);
            } else {
                currentResults = currentResults.concat(response.items || []);
            }
            currentTotal = response.total || 0;
            updateHint('search.found_results', { n: currentTotal });
            if (searchInput) searchInput.placeholder = '';

            updateSearchActionsVisibility();

            const galleryImages = convertResultsToGallery(response.items || []);
            if (onSearchResults) {
                onSearchResults(galleryImages, formatQueryDisplay(), currentTotal, offset > 0);
            }
            scrollLoadEnabled = currentResults.length < currentTotal;
        } catch (err) {
            console.error('[Search] 高级搜索失败:', err);
            showSearchError(err.message || t('search.search_request_failed'));
        } finally {
            setSearching(false);
        }
    }

    function formatQueryDisplay() {
        const parts = [];
        const conditions = collectAdvancedConditions();
        if (conditions.length > 0) {
            parts.push(conditions.map(c => c.value).join(', '));
        } else if (currentTagSearch && currentTagSearch.value) {
            parts.push(paramCatLabel(currentTagSearch.key ? currentTagSearch.key.replace(/^json:/, '') : '') + ': ' + currentTagSearch.value);
        } else if (searchInput) {
            const query = searchInput.value.trim();
            if (query) parts.push(query);
        }
        if (selectedFolders.length > 0) {
            parts.push(selectedFolders.length + t('search.folders_count'));
        }
        if (advDateFrom && advDateFrom.value) {
            parts.push(t('search.from_prefix') + advDateFrom.value);
        }
        return parts.join(' | ') || t('search.advanced_search');
    }

    function setSearching(searching) {
        if (searchBar) {
            searchBar.classList.toggle('searching', searching);
        }
    }

    // 当前提示的状态（i18n key + 参数 / 原始文本），语言切换时据此重新翻译
    let hintState = null;

    function hideHint() {
        if (searchHint) {
            searchHint.textContent = '';
            searchHint.style.display = 'none';
        }
        if (searchBar) searchBar.classList.remove('has-hint');
        hintState = null;
    }

    /** 按 hintState 渲染提示（供语言切换后重渲染） */
    function renderHint() {
        if (!searchHint) return;
        let text = '';
        if (hintState) {
            if (hintState.key) {
                text = t(hintState.key, hintState.params);
                // 翻译缺失时回退到原始文本
                if (text === hintState.key) text = hintState.raw || hintState.key;
            } else {
                text = hintState.raw || '';
            }
        }
        searchHint.textContent = text;
        searchHint.style.display = text ? 'block' : 'none';
        if (searchBar) searchBar.classList.toggle('has-hint', !!text);
    }

    /** 通过 i18n key + 参数设置提示（可随语言切换重新翻译） */
    function updateHint(key, params) {
        hintState = { key: key, params: params };
        renderHint();
    }

    /** 设置原始文本提示（如后端错误信息，不参与翻译） */
    function setHintRaw(raw) {
        hintState = { raw: raw || '' };
        renderHint();
    }

    function updateResultHint(query) {
        if (currentTotal === 0) {
            hintState = { key: 'search.no_results' };
        } else {
            hintState = { key: 'search.found_results', params: { n: currentTotal } };
        }
        renderHint();
    }

    function showSearchError(message, code) {
        let text = message || '';
        // 优先用错误码映射 i18n 文案（后端返回中文时也不串语言）
        if (code) {
            const key = 'search.err_' + code;
            const translated = t(key);
            if (translated !== key) text = translated;
        }
        setHintRaw(text);
        if (typeof App !== 'undefined' && App.showToast) {
            App.showToast(text, 'error');
        }
    }

    // ==================== 加载更多 ====================

    /**
     * 加载更多搜索结果
     * @param {number} [batchSize] - 批次大小，默认 PAGE_SIZE（1000）
     * @returns {Promise<boolean>} 是否成功加载
     */
    async function loadMore(batchSize) {
        if (!isSearchActive) return false;
        if (isLoadingMore) return false;
        if (currentResults.length >= currentTotal) return false;

        batchSize = batchSize || PAGE_SIZE;
        isLoadingMore = true;

        const nextOffset = currentResults.length;

        try {
            if (isAdvancedMode && currentSearchParams) {
                currentSearchParams.offset = nextOffset;
                currentSearchParams.limit = batchSize;
                const response = await WailsBridge.advancedSearch(currentSearchParams);
                if (!response || !response.success) return false;

                const newItems = response.items || [];
                currentResults = currentResults.concat(newItems);
                currentOffset = currentResults.length;

                const newGalleryImages = convertResultsToGallery(newItems);
                if (onSearchResults) {
                    onSearchResults(newGalleryImages, formatQueryDisplay(), currentTotal, true);
                }
                updateSearchActionsVisibility();
                scrollLoadEnabled = currentResults.length < currentTotal;
            } else {
                const response = await WailsBridge.searchImages(currentQuery, '', nextOffset, batchSize);
                if (!response || !response.success) return false;

                const newItems = response.items || [];
                currentResults = currentResults.concat(newItems);
                currentOffset = currentResults.length;

                const newGalleryImages = convertResultsToGallery(newItems);
                if (onSearchResults) {
                    onSearchResults(newGalleryImages, currentQuery, currentTotal, true);
                }
                updateSearchActionsVisibility();
                scrollLoadEnabled = currentResults.length < currentTotal;
            }
            return true;
        } catch (err) {
            console.error('[Search] 加载更多失败:', err);
            return false;
        } finally {
            isLoadingMore = false;
        }
    }

    // ==================== 显示全部结果 ====================

    async function showAllResults() {
        if (!isSearchActive) return;
        if (isLoadingMore) return;

        isLoadingMore = true;
        try {
            while (currentResults.length < currentTotal) {
                try {
                    const nextOffset = currentResults.length;

                    let response;
                    if (isAdvancedMode && currentSearchParams) {
                        currentSearchParams.offset = nextOffset;
                        currentSearchParams.limit = PAGE_SIZE;
                        response = await WailsBridge.advancedSearch(currentSearchParams);
                    } else {
                        response = await WailsBridge.searchImages(currentQuery, '', nextOffset, PAGE_SIZE);
                    }

                    if (!response || !response.success) break;

                    const newItems = response.items || [];
                    if (newItems.length === 0) break;

                    currentResults = currentResults.concat(newItems);
                    currentOffset = currentResults.length;

                    const newGalleryImages = convertResultsToGallery(newItems);
                    if (onSearchResults) {
                        onSearchResults(newGalleryImages, formatQueryDisplay(), currentTotal, true);
                    }
                } catch (err) {
                    console.error('[Search] 加载全部失败:', err);
                    break;
                }
            }
        } finally {
            isLoadingMore = false;
        }

        updateSearchActionsVisibility();

        if (typeof Gallery !== 'undefined' && Gallery.renderAllSearchResults) {
            Gallery.renderAllSearchResults();
        }
    }

    // ==================== 滚动自动加载 ====================

    function initScrollLoad() {
        const galleryScroll = document.getElementById('galleryScroll');
        if (!galleryScroll) return;

        galleryScroll.addEventListener('scroll', () => {
            if (!isSearchActive || !scrollLoadEnabled) return;
            if (isLoadingMore) return;
            if (currentResults.length >= currentTotal) return;

            const { scrollTop, scrollHeight, clientHeight } = galleryScroll;
            // 距离底部还有一页高度时提前触发加载
            if (scrollTop + clientHeight >= scrollHeight - clientHeight) {
                loadMore(SCROLL_LOAD_SIZE);
            }
        });
    }

    // ==================== 按钮可见性 ====================

    function updateSearchActionsVisibility() {
        if (!searchActions) return;

        const hasMore = currentResults.length < currentTotal;

        if (isSearchActive && currentTotal > 0) {
            searchActions.style.display = (hasMore || currentTotal > PAGE_SIZE) ? 'flex' : 'none';
        } else {
            searchActions.style.display = 'none';
        }

        if (searchShowAllBtn) {
            searchShowAllBtn.style.display = hasMore ? 'inline-block' : 'none';
        }
        if (searchLoadMoreBtn) {
            searchLoadMoreBtn.style.display = hasMore ? 'inline-block' : 'none';
        }
    }

    // ==================== 退出搜索 ====================

    function exitSearch() {
        if (!isSearchActive) return;

        isSearchActive = false;
        isAdvancedMode = false;
        currentQuery = '';
        currentResults = [];
        currentTotal = 0;
        currentOffset = 0;
        currentSearchParams = null;
        currentTagSearch = null;
        scrollLoadEnabled = false;
        isLoadingMore = false;

        if (searchInput) { searchInput.value = ''; searchInput.placeholder = t('search.placeholder'); }
        if (searchClearBtn) searchClearBtn.style.display = 'none';
        hideHint();
        if (searchBar) searchBar.classList.remove('searching');
        if (searchActions) searchActions.style.display = 'none';
        if (debounceTimer) { clearTimeout(debounceTimer); debounceTimer = null; }

        if (onClearSearch) onClearSearch();
    }

    // ==================== 结果转换 ====================

    function convertResultsToGallery(results) {
        if (!results || results.length === 0) return [];

        return results.map(item => ({
            id: item.id,
            path: item.path,
            name: item.name,
            size: item.size,
            lastModified: item.lastModified,
            createdAt: item.createdAt || 0,
            folder: item.folder,
            rootPath: item.rootPath,
            url: null,
            thumbnailUrl: null,
            displayName: item.rootPath ? item.rootPath.split(/[\\/]/).pop() : '',
            metadata: null,
            file: null,
            _loaded: false,
            _fromServer: true,
            _searchResult: true,
            prompt: item.prompt,
            negativePrompt: item.negativePrompt,
            paramsJson: item.paramsJson
        }));
    }

    // ==================== 公开 API ====================

    return {
        init,
        initAdvancedUI,
        isSearchActive: () => isSearchActive,
        getCurrentQuery: () => formatQueryDisplay() || currentQuery,
        getCurrentTotal: () => currentTotal,
        // 当前生效的关键词列表（供详情/卡片高亮命中词；exclude 条件不参与高亮）
        getCurrentKeywords: () => {
            if (isAdvancedMode) {
                const kws = [];
                for (const c of collectAdvancedConditions()) {
                    if (c.mode !== 'exclude') kws.push(c.value);
                }
                // 生成参数标签搜索：把标签值加入高亮词
                if (currentTagSearch && currentTagSearch.value) {
                    kws.push(currentTagSearch.value);
                }
                return kws;
            }
            return currentQuery ? [currentQuery] : [];
        },
        loadMore,
        exitSearch,
        performSearch,
        focus: () => { if (searchInput) searchInput.focus(); }
    };
})();
