//! A least-recently-used cache with a fixed capacity.

use std::collections::{HashMap, VecDeque};

pub struct LruCache<V> {
    capacity: usize,
    entries: HashMap<String, V>,
    order: VecDeque<String>,
}

impl<V> LruCache<V> {
    pub fn new(capacity: usize) -> Self {
        LruCache { capacity, entries: HashMap::new(), order: VecDeque::new() }
    }

    /// Inserts a value, evicting the least recently used one when full.
    pub fn put(&mut self, key: String, value: V) {
        if self.entries.len() == self.capacity {
            if let Some(oldest) = self.order.pop_front() {
                self.entries.remove(&oldest);
            }
        }
        self.order.push_back(key.clone());
        self.entries.insert(key, value);
    }

    pub fn get(&mut self, key: &str) -> Option<&V> {
        self.order.retain(|k| k != key);
        self.order.push_back(key.to_string());
        self.entries.get(key)
    }
}
