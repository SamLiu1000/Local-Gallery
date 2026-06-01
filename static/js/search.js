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
    let advDateFrom;
    let advDateTo;
    let conditionsList;
    let addConditionBtn;
    let advSearchApply;
    let advSearchReset;
    let advSearchFoldersCheck;
    let conditionRowCount = 1;

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
        initScrollLoad();
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

    // ==================== 高级搜索 UI 初始化 ====================

    function initAdvancedUI() {
        advancedSearchToggle = document.getElementById('advancedSearchToggle');
        advancedSearchPanel = document.getElementById('advancedSearchPanel');
        advMatchMode = document.getElementById('advMatchMode');
        folderCheckboxList = document.getElementById('folderCheckboxList');
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
            if (!visible) {
                populateFolderCheckboxes();
            }
        });

        // 添加条件
        addConditionBtn.addEventListener('click', () => {
            conditionRowCount++;
            const row = document.createElement('div');
            row.className = 'condition-row';
            row.id = 'conditionRow' + conditionRowCount;
            row.innerHTML = `
                <select class="condition-field">
                    <option value="all">全部字段</option>
                    <option value="path">路径</option>
                    <option value="prompt">提示词</option>
                    <option value="negative_prompt">反向提示词</option>
                    <option value="params_json">参数</option>
                    <option value="folder_name">文件夹名称</option>
                </select>
                <select class="condition-mode">
                    <option value="contains">包含</option>
                    <option value="exact">精确匹配</option>
                    <option value="exclude">排除</option>
                    <option value="word">全词匹配</option>
                </select>
                <input type="text" class="condition-value" placeholder="输入关键词..." />
                <button class="condition-remove-btn btn-small"><span class="icon icon-close"></span></button>
            `;
            row.querySelector('.condition-remove-btn').addEventListener('click', (e) => {
                e.stopPropagation();
                row.remove();
            });
            conditionsList.appendChild(row);
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
            if (folderCheckboxList) populateFolderCheckboxes();
            if (advDateFrom) advDateFrom.value = '';
            if (advDateTo) advDateTo.value = '';
            if (advSearchFoldersCheck) advSearchFoldersCheck.checked = false;
            // 移除所有额外条件行，只保留第一个
            conditionRowCount = 1;
            if (conditionsList) {
                conditionsList.innerHTML = `
                    <div class="condition-row" id="conditionRow0">
                        <select class="condition-field">
                            <option value="all">全部字段</option>
                            <option value="path">路径</option>
                            <option value="prompt">提示词</option>
                            <option value="negative_prompt">反向提示词</option>
                            <option value="params_json">参数</option>
                            <option value="folder_name">文件夹名称</option>
                        </select>
                        <select class="condition-mode">
                            <option value="contains">包含</option>
                            <option value="exact">精确匹配</option>
                            <option value="exclude">排除</option>
                            <option value="word">全词匹配</option>
                        </select>
                        <input type="text" class="condition-value" placeholder="输入关键词..." />
                        <button class="condition-remove-btn btn-small" style="display:none;"><span class="icon icon-close"></span></button>
                    </div>
                `;
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

        if (roots.length === 0) {
            folderCheckboxList.innerHTML = '<div class="folder-dropdown-empty">' + t('search.no_matching_folders') + '</div>';
            return;
        }

        // 按导航栏顺序排序
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
            folderCheckboxList.appendChild(label);
        }
    }

    /**
     * 按导航栏文件夹顺序排序，通过读取 sidebar 的排序设置
     */
    async function sortRootsBySidebarOrder(roots) {
        try {
            let orderList = [];
            // 从后端读取 sidebar 的文件夹排序
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

        setSearching(true);
        updateHint('正在搜索...');

        try {
            const response = await WailsBridge.searchImages(query, '', 0, PAGE_SIZE);
            if (!response || !response.success) {
                showSearchError(response?.message || '搜索失败');
                return;
            }

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
            showSearchError(err.message || '搜索请求失败');
        } finally {
            setSearching(false);
        }
    }

    async function performAdvancedSearch(offset) {
        const params = buildAdvancedSearchParams(offset, PAGE_SIZE);
        currentSearchParams = params;
        currentOffset = offset;
        isSearchActive = true;
        isAdvancedMode = true;

        setSearching(true);
        updateHint('正在高级搜索...');

        try {
            console.log('[Search] 调用 WailsBridge.advancedSearch, params:', JSON.stringify(params));
            const response = await WailsBridge.advancedSearch(params);
            console.log('[Search] advancedSearch 返回:', JSON.stringify({ success: response?.success, total: response?.total, message: response?.message, itemsCount: response?.items?.length }));

            if (!response || !response.success) {
                showSearchError(response?.message || '搜索失败');
                return;
            }

            if (offset === 0) {
                currentResults = response.items || [];
            } else {
                currentResults = currentResults.concat(response.items || []);
            }
            currentTotal = response.total || 0;
            updateHint(`找到 ${currentTotal} 结果`);
            if (searchInput) searchInput.placeholder = '';

            updateSearchActionsVisibility();

            const galleryImages = convertResultsToGallery(response.items || []);
            if (onSearchResults) {
                onSearchResults(galleryImages, formatQueryDisplay(), currentTotal, offset > 0);
            }
            scrollLoadEnabled = currentResults.length < currentTotal;
        } catch (err) {
            console.error('[Search] 高级搜索失败:', err);
            showSearchError(err.message || '搜索请求失败');
        } finally {
            setSearching(false);
        }
    }

    function formatQueryDisplay() {
        const parts = [];
        const conditions = collectAdvancedConditions();
        if (conditions.length > 0) {
            parts.push(conditions.map(c => c.value).join(', '));
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

    function hideHint() {
        if (searchHint) {
            searchHint.textContent = '';
            searchHint.style.display = 'none';
        }
    }

    function updateHint(text) {
        if (searchHint) {
            searchHint.textContent = text;
            searchHint.style.display = 'block';
        }
    }

    function updateResultHint(query) {
        if (!searchHint) return;
        if (currentTotal === 0) {
            searchHint.textContent = t('search.no_results');
        } else {
            searchHint.textContent = t('search.found_results').replace('{n}', currentTotal);
        }
    }

    function showSearchError(message) {
        if (searchHint) {
            searchHint.textContent = message;
            searchHint.style.display = 'block';
        }
        if (typeof App !== 'undefined' && App.showToast) {
            App.showToast(message, 'error');
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
        scrollLoadEnabled = false;
        isLoadingMore = false;

        if (searchInput) { searchInput.value = ''; searchInput.placeholder = '搜索前请先对文件夹索引'; }
        if (searchClearBtn) searchClearBtn.style.display = 'none';
        if (searchHint) { searchHint.textContent = ''; searchHint.style.display = 'none'; }
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
        loadMore,
        exitSearch,
        performSearch,
        focus: () => { if (searchInput) searchInput.focus(); }
    };
})();
