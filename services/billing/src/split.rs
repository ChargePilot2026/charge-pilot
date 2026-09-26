//! 多方分账:模式 A(全分账) / 模式 B(仅服务费)
//!
//! 按 split_party.ratio_bp(万分比,合计 10000)分配金额
//! 金额单位:分

use serde::{Deserialize, Serialize};

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct Party {
    pub id: u64,
    pub party_code: String,
    pub ratio_bp: u32,    // basis point,合计 10000
}

pub fn split_pool(pool_cents: i64, parties: &[Party]) -> Vec<(Party, i64)> {
    // 按比例分配;尾差归最后一个
    let mut allocated = 0i64;
    let mut out = Vec::with_capacity(parties.len());
    for (i, p) in parties.iter().enumerate() {
        let amt = if i + 1 == parties.len() {
            pool_cents - allocated
        } else {
            (pool_cents as i128 * p.ratio_bp as i128 / 10000) as i64
        };
        allocated += amt;
        out.push((p.clone(), amt));
    }
    out
}

#[cfg(test)]
mod tests {
    use super::*;
    #[test]
    fn split_10000_to_two_50_50() {
        let parties = vec![
            Party { id: 1, party_code: "a".into(), ratio_bp: 5000 },
            Party { id: 2, party_code: "b".into(), ratio_bp: 5000 },
        ];
        let r = split_pool(10000, &parties);
        assert_eq!(r[0].1, 5000);
        assert_eq!(r[1].1, 5000);
    }
    #[test]
    fn split_remainder_to_last() {
        let parties = vec![
            Party { id: 1, party_code: "a".into(), ratio_bp: 3333 },
            Party { id: 2, party_code: "b".into(), ratio_bp: 3333 },
            Party { id: 3, party_code: "c".into(), ratio_bp: 3334 },
        ];
        let r = split_pool(10000, &parties);
        assert_eq!(r[0].1, 3333);
        assert_eq!(r[1].1, 3333);
        assert_eq!(r[2].1, 3334);
        assert_eq!(r.iter().map(|x| x.1).sum::<i64>(), 10000);
    }
}