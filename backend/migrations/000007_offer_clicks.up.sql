CREATE TABLE offer_clicks (
    id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    offer_id BIGINT UNSIGNED NOT NULL,
    clicked_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    PRIMARY KEY (id),
    INDEX idx_offer_clicks_offer_time (offer_id, clicked_at),
    CONSTRAINT fk_offer_clicks_offer FOREIGN KEY (offer_id)
        REFERENCES offers(id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
