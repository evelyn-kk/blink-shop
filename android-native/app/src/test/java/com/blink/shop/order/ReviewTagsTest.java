package com.blink.shop.order;

import static org.junit.Assert.assertEquals;

import java.util.Arrays;
import java.util.Collections;

import org.junit.Test;

public class ReviewTagsTest {

    @Test
    public void splitsOnSpacesAndCommasAndDedupes() {
        assertEquals(Arrays.asList("音质好", "续航长", "轻"), ReviewActivity.splitTags(" 音质好，续航长 , 轻、音质好 "));
        assertEquals(Collections.emptyList(), ReviewActivity.splitTags("  ，, "));
    }
}
