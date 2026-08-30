import json
from pathlib import Path
from typing import Optional

# 数据存储目录
DATA_DIR = Path(__file__).parent / "data"

def ensure_data_dir() -> None:
    # 确保数据目录存在
    DATA_DIR.mkdir(parents=True, exist_ok=True)


def get_user_file_path(uid: str) -> Path:
    # 获取用户数据文件路径
    return DATA_DIR / f"{uid}.json"

def extract_uid_from_data(gacha_data: dict) -> Optional[str]:
    # 从抽卡数据中提取 UID
    # 参数: gacha_data - 卡池数据字典，格式为 {gacha_type: {raw_data: [...]}}
    # 返回: UID 字符串，如果无法提取则返回 None
    for pool_data in gacha_data.values():
        raw_data = pool_data.get('raw_data', [])
        if raw_data and len(raw_data) > 0:
            first_item = raw_data[0]
            uid = first_item.get('uid')
            if uid:
                return str(uid)
    return None


def load_user_data(uid: str) -> dict:
    # 加载用户的抽卡记录
    # 参数: uid - 用户 UID
    # 返回: 用户数据字典，格式为 {gacha_type: [item1, item2, ...]}
    file_path = get_user_file_path(uid)
    
    if not file_path.exists():
        return {}
    
    try:
        with open(file_path, 'r', encoding='utf-8') as f:
            return json.load(f)
    except (json.JSONDecodeError, IOError) as e:
        print(f"加载用户数据失败 [{uid}]: {e}")
        return {}


def save_user_data(uid: str, data: dict) -> bool:
    # 保存用户的抽卡记录
    # 参数: uid - 用户 UID, data - 用户数据字典
    # 返回: 是否保存成功
    ensure_data_dir()
    file_path = get_user_file_path(uid)
    
    try:
        with open(file_path, 'w', encoding='utf-8') as f:
            json.dump(data, f, ensure_ascii=False, indent=2)
        return True
    except IOError as e:
        print(f"保存用户数据失败 [{uid}]: {e}")
        return False

def create_record_key(item: dict) -> str:
    # 创建记录的唯一标识键（名称+时间+卡池类型）
    return f"{item.get('name', '')}_{item.get('time', '')}_{item.get('gacha_type', '')}"


def merge_gacha_lists(existing: list, new: list) -> tuple[list, int]:
    # 合并两个抽卡记录列表（增量更新）
    # 参数: existing - 已有的记录列表, new - 新获取的记录列表
    # 返回: (合并后的列表, 新增记录数量)
    if not existing:
        return new.copy(), len(new)
    
    if not new:
        return existing.copy(), 0
    
    # 创建已有记录的键集合
    existing_keys = {create_record_key(item) for item in existing}
    
    # 找出新记录中不存在的条目
    new_items = []
    for item in new:
        key = create_record_key(item)
        if key not in existing_keys:
            new_items.append(item)
    
    if not new_items:
        return existing.copy(), 0
    
    # 合并记录并按时间降序排序
    merged = existing.copy()
    merged.extend(new_items)
    merged.sort(key=lambda x: x.get('time', ''), reverse=True)
    
    return merged, len(new_items)


def merge_user_data(uid: str, new_gacha_data: dict) -> tuple[dict, dict[str, int]]:
    # 合并用户数据（增量更新）
    # 参数: uid - 用户 UID, new_gacha_data - 新获取的卡池数据
    # 返回: (合并后的完整数据, 各卡池新增数量统计)
    existing_data = load_user_data(uid)
    
    merged_data = {}
    new_counts = {}
    
    for gacha_type, pool_data in new_gacha_data.items():
        new_raw = pool_data.get('raw_data', [])
        existing_raw = existing_data.get(gacha_type, [])
        
        merged_raw, new_count = merge_gacha_lists(existing_raw, new_raw)
        new_counts[gacha_type] = new_count
        merged_data[gacha_type] = merged_raw
    
    return merged_data, new_counts

def get_saved_uids() -> list[dict]:
    # 获取所有已保存的用户 UID 列表
    # 返回: 用户信息列表，格式为 [{uid, last_update, total_records}, ...]
    ensure_data_dir()
    
    users = []
    
    for file_path in DATA_DIR.glob("*.json"):
        uid = file_path.stem
        
        try:
            stat = file_path.stat()
            last_update = stat.st_mtime
            
            with open(file_path, 'r', encoding='utf-8') as f:
                data = json.load(f)
            
            # 统计总记录数
            total_records = sum(
                len(pool_data) if isinstance(pool_data, list) else 0
                for pool_data in data.values()
            )
            
            users.append({
                'uid': uid,
                'last_update': last_update,
                'total_records': total_records
            })
        except Exception as e:
            print(f"读取用户文件失败 [{file_path}]: {e}")
    
    # 按最后更新时间降序排列
    users.sort(key=lambda x: x['last_update'], reverse=True)
    return users


def delete_user_data(uid: str) -> bool:
    # 删除用户的抽卡记录
    # 参数: uid - 用户 UID
    # 返回: 是否删除成功
    file_path = get_user_file_path(uid)
    
    if not file_path.exists():
        return False
    
    try:
        file_path.unlink()
        return True
    except IOError as e:
        print(f"删除用户数据失败 [{uid}]: {e}")
        return False